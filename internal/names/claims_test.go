package names

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	claimAnchor      = "0123456789abcdef0123456789abcdef"
	claimOtherAnchor = "ffff6789abcdef0123456789abcdef00"
	claimPeer        = "peer-tester.testsite.netbird.example.net"
)

type claimFixture struct {
	rootKey  *ecdsa.PrivateKey
	leafKey  *ecdsa.PrivateKey
	leafPEM  string
	rootPubs []*ecdsa.PublicKey
}

func mintClaimFixture(t *testing.T, cn string) *claimFixture {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, rootCert, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
	return &claimFixture{
		rootKey:  rootKey,
		leafKey:  leafKey,
		leafPEM:  leafPEM,
		rootPubs: []*ecdsa.PublicKey{&rootKey.PublicKey},
	}
}

func (f *claimFixture) envelope(t *testing.T, name, anchor string, issuedAt time.Time, ttl uint64) *ClaimEnvelope {
	t.Helper()
	artifact, err := json.Marshal(ClaimArtifact{
		Format:       ClaimFormat,
		Name:         name,
		IP:           "100.64.9.9",
		Anchor:       anchor,
		IssuedAtUnix: issuedAt.Unix(),
		TTLSeconds:   ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	sig, err := ecdsa.SignASN1(rand.Reader, f.leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return &ClaimEnvelope{
		ArtifactB64: base64.StdEncoding.EncodeToString(artifact),
		SigB64:      base64.StdEncoding.EncodeToString(sig),
		LeafPEM:     f.leafPEM,
	}
}

func snapWithBinding(name, anchor string) *Snapshot {
	return &Snapshot{
		Format:          SnapshotFormat,
		SiteID:          "testsite",
		Zone:            "netbird.example.net",
		Serial:          4,
		GeneratedAtUnix: time.Now().Add(-2 * time.Hour).Unix(),
		TTLSeconds:      2_592_000,
		Records:         []Record{{Name: "portal.testsite.netbird.example.net", IP: "100.64.0.10"}},
		PeerBindings:    []PeerBinding{{Name: name, Anchor: anchor}},
	}
}

func TestVerifyClaim(t *testing.T) {
	fix := mintClaimFixture(t, claimAnchor)
	now := time.Now()
	snap := snapWithBinding(claimPeer, claimAnchor)

	vc, err := VerifyClaim(fix.envelope(t, claimPeer, claimAnchor, now.Add(-time.Minute), 3600), snap, fix.rootPubs, now)
	if err != nil {
		t.Fatalf("valid claim refused: %v", err)
	}
	if vc.Name != claimPeer || vc.IP.String() != "100.64.9.9" {
		t.Fatalf("claim = %+v", vc)
	}

	for _, tc := range []struct {
		name    string
		env     *ClaimEnvelope
		snap    *Snapshot
		roots   []*ecdsa.PublicKey
		wantErr string
	}{
		{"cross-claim", fix.envelope(t, claimPeer, claimAnchor, now.Add(-time.Minute), 3600),
			snapWithBinding(claimPeer, claimOtherAnchor), fix.rootPubs, "different anchor"},
		{"service name unclaimed", fix.envelope(t, "portal.testsite.netbird.example.net", claimAnchor, now.Add(-time.Minute), 3600),
			snap, fix.rootPubs, "no binding"},
		{"expired", fix.envelope(t, claimPeer, claimAnchor, now.Add(-2*time.Hour), 3600),
			snap, fix.rootPubs, "expired"},
		{"identity mismatch", fix.envelope(t, claimPeer, claimOtherAnchor, now.Add(-time.Minute), 3600),
			snapWithBinding(claimPeer, claimOtherAnchor), fix.rootPubs, "does not match claimed anchor"},
		{"wrong root", fix.envelope(t, claimPeer, claimAnchor, now.Add(-time.Minute), 3600),
			snap, mintClaimFixture(t, claimAnchor).rootPubs, "not signed by a trust root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyClaim(tc.env, tc.snap, tc.roots, now)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	// Tampered artifact refused.
	env := fix.envelope(t, claimPeer, claimAnchor, now.Add(-time.Minute), 3600)
	raw, _ := base64.StdEncoding.DecodeString(env.ArtifactB64)
	env.ArtifactB64 = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), "100.64.9.9", "6.6.6.6", 1)))
	if _, err := VerifyClaim(env, snap, fix.rootPubs, now); err == nil ||
		!strings.Contains(err.Error(), "signature invalid") {
		t.Fatalf("tampered artifact: err = %v", err)
	}
}

func TestStoreClaimsRoundTripAndLookup(t *testing.T) {
	fix := mintClaimFixture(t, claimAnchor)
	now := time.Now()
	snap := snapWithBinding(claimPeer, claimAnchor)
	st, err := NewStore(t.TempDir(), fix.rootPubs)
	if err != nil {
		t.Fatal(err)
	}
	envs := []ClaimEnvelope{
		*fix.envelope(t, claimPeer, claimAnchor, now.Add(-30*time.Minute), 3600),
		*fix.envelope(t, claimPeer, claimAnchor, now.Add(-time.Minute), 3600), // fresher wins
	}
	body, _ := json.Marshal(envs)
	if err := st.PutClaims(body); err != nil {
		t.Fatal(err)
	}
	if n := st.ClaimCount(); n != 2 {
		t.Fatalf("claim count = %d", n)
	}
	vc := st.LookupClaim(claimPeer+".", snap, now) // trailing dot tolerated
	if vc == nil || vc.Age > 2*time.Minute {
		t.Fatalf("lookup = %+v, want the fresher claim", vc)
	}
	// Garbage body refused; store keeps serving the last good file.
	if err := st.PutClaims([]byte("not json")); err == nil {
		t.Fatal("garbage claims.json must be refused")
	}
	if st.LookupClaim(claimPeer, snap, now) == nil {
		t.Fatal("last good claims file must survive a refused Put")
	}
}
