package names

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const testHost = "portal.testsite.netbird.example.net"

func mintKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func signArtifact(t *testing.T, key *ecdsa.PrivateKey, artifact []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(artifact)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(der) + "\n")
}

func fixtureArtifact(t *testing.T, serial uint64, generatedAt int64, ttl uint64, records []Record) []byte {
	t.Helper()
	b, err := json.Marshal(Snapshot{
		Format:          SnapshotFormat,
		SiteID:          "testsite",
		Zone:            "netbird.example.net",
		Serial:          serial,
		GeneratedAtUnix: generatedAt,
		TTLSeconds:      ttl,
		CAID:            "test-ca",
		Records:         records,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestVerifyAndParse(t *testing.T) {
	key := mintKey(t)
	other := mintKey(t)
	artifact := fixtureArtifact(t, 3, 1000, 100, []Record{{Name: testHost, IP: "100.64.0.10"}})
	sig := signArtifact(t, key, artifact)

	for _, tc := range []struct {
		name     string
		artifact []byte
		sig      []byte
		roots    []*ecdsa.PublicKey
		wantErr  string
	}{
		{"valid pair verifies", artifact, sig, []*ecdsa.PublicKey{&key.PublicKey}, ""},
		{"second root verifies", artifact, sig, []*ecdsa.PublicKey{&other.PublicKey, &key.PublicKey}, ""},
		{"wrong key refused", artifact, sig, []*ecdsa.PublicKey{&other.PublicKey}, "signature invalid"},
		{"no roots refused", artifact, sig, nil, "no trust roots"},
		{"tampered artifact refused", []byte(strings.Replace(string(artifact), "100.64.0.10", "6.6.6.6", 1)), sig, []*ecdsa.PublicKey{&key.PublicKey}, "signature invalid"},
		{"garbage sig refused", artifact, []byte("!!!"), []*ecdsa.PublicKey{&key.PublicKey}, "not base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := VerifyAndParse(tc.artifact, tc.sig, tc.roots)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if snap.Serial != 3 {
					t.Fatalf("serial = %d, want 3", snap.Serial)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestGrading(t *testing.T) {
	snap := Snapshot{GeneratedAtUnix: 1_000_000, TTLSeconds: 2_592_000, Serial: 7}
	at := func(offset time.Duration) Grade {
		return snap.GradeAt(time.Unix(1_000_000, 0).Add(offset)).Grade
	}
	if g := at(24 * time.Hour); g != GradeFresh {
		t.Fatalf("1d = %s, want fresh", g)
	}
	if g := at(FreshBound); g != GradeAging {
		t.Fatalf("7d = %s, want aging", g)
	}
	if g := at(2_592_000 * time.Second); g != GradeExpired {
		t.Fatalf("ttl = %s, want expired", g)
	}
	zeroed := snap
	zeroed.GeneratedAtUnix = 0
	if g := zeroed.GradeAt(time.Unix(1_000, 0)).Grade; g != GradeExpired {
		t.Fatalf("zero generated_at = %s, want expired", g)
	}
}

func TestPickFallback(t *testing.T) {
	now := time.Unix(2_000, 0)
	snapWith := func(records []Record) *Snapshot {
		return &Snapshot{
			Format: SnapshotFormat, Serial: 5, GeneratedAtUnix: 1_000,
			TTLSeconds: 2_592_000, Records: records,
		}
	}
	rec := []Record{{Name: testHost, IP: "100.64.0.10"}}

	t.Run("snapshot wins without observation", func(t *testing.T) {
		ans, err := PickFallback(snapWith(rec), nil, nil, testHost, now)
		if err != nil || ans.Source != SourceSnapshot || ans.IP.String() != "100.64.0.10" {
			t.Fatalf("got %+v, %v", ans, err)
		}
	})
	t.Run("observed gap-fills absent names", func(t *testing.T) {
		obs := &ObservedRecord{Name: testHost, IP: "100.64.0.99", ObservedAt: 1_500}
		ans, err := PickFallback(snapWith(nil), nil, obs, testHost, now)
		if err != nil || ans.Source != SourceObserved || ans.IP.String() != "100.64.0.99" {
			t.Fatalf("got %+v, %v", ans, err)
		}
	})
	t.Run("fresher observation supersedes conflicting snapshot record", func(t *testing.T) {
		obs := &ObservedRecord{Name: testHost, IP: "100.64.0.77", ObservedAt: 1_500}
		ans, err := PickFallback(snapWith(rec), nil, obs, testHost, now)
		if err != nil || ans.Source != SourceObserved || ans.IP.String() != "100.64.0.77" {
			t.Fatalf("got %+v, %v", ans, err)
		}
	})
	t.Run("older observation loses to snapshot", func(t *testing.T) {
		obs := &ObservedRecord{Name: testHost, IP: "100.64.0.77", ObservedAt: 900}
		ans, err := PickFallback(snapWith(rec), nil, obs, testHost, now)
		if err != nil || ans.Source != SourceSnapshot || ans.IP.String() != "100.64.0.10" {
			t.Fatalf("got %+v, %v", ans, err)
		}
	})
	t.Run("observed serves when snapshot unavailable", func(t *testing.T) {
		obs := &ObservedRecord{Name: testHost, IP: "100.64.0.42", ObservedAt: 1_500}
		ans, err := PickFallback(nil, ErrBadSignature, obs, testHost, now)
		if err != nil || ans.Source != SourceObserved {
			t.Fatalf("got %+v, %v", ans, err)
		}
	})
	t.Run("expired snapshot refused, both reasons on empty", func(t *testing.T) {
		expired := snapWith(rec)
		expired.TTLSeconds = 100
		_, err := PickFallback(expired, nil, nil, testHost, now)
		if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "no observed record") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestStoreSnapshotMonotonicAndTamper(t *testing.T) {
	key := mintKey(t)
	st, err := NewStore(t.TempDir(), []*ecdsa.PublicKey{&key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	s2 := fixtureArtifact(t, 2, 1000, 2_592_000, nil)
	if snap, err := st.PutSnapshot(s2, signArtifact(t, key, s2)); err != nil || snap == nil || snap.Serial != 2 {
		t.Fatalf("put serial 2: %+v, %v", snap, err)
	}
	// A lower serial never replaces a higher one.
	s1 := fixtureArtifact(t, 1, 1500, 2_592_000, nil)
	if _, err := st.PutSnapshot(s1, signArtifact(t, key, s1)); !errors.Is(err, ErrNotNewer) {
		t.Fatalf("lower serial must return ErrNotNewer, got %v", err)
	}
	if got, err := st.LoadSnapshot(); err != nil || got.Serial != 2 {
		t.Fatalf("cache = %+v, %v; want serial 2", got, err)
	}
	// An unverifiable candidate is refused.
	s3 := fixtureArtifact(t, 3, 2000, 2_592_000, nil)
	if _, err := st.PutSnapshot(s3, []byte(base64.StdEncoding.EncodeToString([]byte("junk")))); err == nil {
		t.Fatal("bad signature must refuse")
	}
}

func TestObservedStoreRoundtripPruneCap(t *testing.T) {
	st, err := NewStore(t.TempDir(), []*ecdsa.PublicKey{&mintKey(t).PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10_000_000, 0)
	_ = st.RecordObservation("A.example.", "100.64.0.1", now.Add(-time.Minute))
	_ = st.RecordObservation("a.EXAMPLE", "100.64.0.9", now) // upsert, case-insensitive + dot-tolerant
	hit := st.LookupObserved("a.example", now)
	if hit == nil || hit.IP != "100.64.0.9" {
		t.Fatalf("lookup = %+v", hit)
	}
	// TTL prune: too-old entries are invisible and swept on next write.
	_ = st.RecordObservation("old.example", "100.64.0.3", now.Add(-ObservedTTL-time.Second))
	if st.LookupObserved("old.example", now) != nil {
		t.Fatal("expired observation must be invisible")
	}
	if n := st.ObservedCount(now); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

// TestForwarderEndToEnd exercises the full device-wide path over a real
// UDP socket: a live upstream answers (and feeds the observed tier);
// the upstream dies → the signed snapshot answers; the snapshot lacks a
// name → the observed tier answers; nothing knows the name → SERVFAIL.
func TestForwarderEndToEnd(t *testing.T) {
	key := mintKey(t)
	st, err := NewStore(t.TempDir(), []*ecdsa.PublicKey{&key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}

	// A real upstream DNS server that answers one name, then "dies".
	var upstreamDead atomic.Bool
	up := &dns.Server{Addr: "127.0.0.1:0", Net: "udp", Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		reply := new(dns.Msg)
		if upstreamDead.Load() {
			return // no answer at all — the dead-resolver shape
		}
		reply.SetReply(req)
		if req.Question[0].Name == "live.testsite.netbird.example.net." {
			reply.Answer = append(reply.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   []byte{100, 64, 0, 50},
			})
		}
		_ = w.WriteMsg(reply)
	})}
	upConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up.PacketConn = upConn
	up.Addr = ""
	go func() { _ = up.ActivateAndServe() }()
	defer up.Shutdown() //nolint:errcheck

	// Signed snapshot with the portal name.
	artifact := fixtureArtifact(t, 4, time.Now().Add(-2*time.Hour).Unix(), 2_592_000,
		[]Record{{Name: testHost, IP: "100.64.0.10"}})
	if _, err := st.PutSnapshot(artifact, signArtifact(t, key, artifact)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fwd := NewForwarder(st, func() []string { return []string{upConn.LocalAddr().String()} }, 500*time.Millisecond)
	addr, err := fwd.Start(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	query := func(name string) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), dns.TypeA)
		c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
		resp, _, err := c.Exchange(m, addr)
		return resp, err
	}
	firstA := func(m *dns.Msg) string {
		for _, rr := range m.Answer {
			if a, ok := rr.(*dns.A); ok {
				return a.A.String()
			}
		}
		return ""
	}

	// 1. Live upstream answers; the observed tier learns the binding.
	resp, err := query("live.testsite.netbird.example.net")
	if err != nil || firstA(resp) != "100.64.0.50" {
		t.Fatalf("live answer = %v, %v", resp, err)
	}
	if st.LookupObserved("live.testsite.netbird.example.net", time.Now()) == nil {
		t.Fatal("live answer must feed the observed tier")
	}

	// 2. Upstream dies → the signed snapshot answers the canonical name.
	upstreamDead.Store(true)
	resp, err = query(testHost)
	if err != nil || firstA(resp) != "100.64.0.10" {
		t.Fatalf("snapshot fallback = %v, %v", resp, err)
	}

	// 3. A live-only name (absent from the snapshot) answers from the
	// NONCANONICAL observed tier.
	resp, err = query("live.testsite.netbird.example.net")
	if err != nil || firstA(resp) != "100.64.0.50" {
		t.Fatalf("observed fallback = %v, %v", resp, err)
	}

	// 4. A name nothing knows → SERVFAIL, never a fabricated answer.
	resp, err = query("unknown.testsite.netbird.example.net")
	if err != nil || resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unknown name = %v, %v; want SERVFAIL", resp, err)
	}

	if served, last := fwd.FallbackStats(); served != 2 || last.IsZero() {
		t.Fatalf("fallback stats = %d, %v; want 2 answers", served, last)
	}
}

func TestRefreshOverHTTP(t *testing.T) {
	key := mintKey(t)
	st, err := NewStore(t.TempDir(), []*ecdsa.PublicKey{&key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	artifact := fixtureArtifact(t, 9, time.Now().Unix(), 2_592_000, []Record{{Name: testHost, IP: "100.64.0.10"}})
	sig := signArtifact(t, key, artifact)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + SnapshotFile:
			_, _ = w.Write(artifact)
		case "/" + SigFile:
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	snap, err := st.Refresh(context.Background(), srv.Client(), srv.URL)
	if err != nil || snap == nil || snap.Serial != 9 {
		t.Fatalf("refresh = %+v, %v", snap, err)
	}
	// Second refresh: same serial → kept cache, signaled as ErrNotNewer.
	snap, err = st.Refresh(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, ErrNotNewer) || snap != nil {
		t.Fatalf("same-serial refresh = %+v, %v; want ErrNotNewer keep", snap, err)
	}
}

func TestGetBaseURL(t *testing.T) {
	if got := GetBaseURL("efdi", "netbird.efdi-backbone.net"); got != "https://get.efdi.netbird.efdi-backbone.net" {
		t.Fatalf("got %q", got)
	}
	if got := GetBaseURL("", "zone"); got != "" {
		t.Fatalf("empty site must yield empty, got %q", got)
	}
}
