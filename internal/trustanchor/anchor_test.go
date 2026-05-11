package trustanchor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// fixedClock returns a closure suitable for Set.withClock that always
// reports the same wall-clock value.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// genAnchor produces an Anchor whose validity window contains `at`. The
// caller can pass a custom clock when constructing the Set so signing
// and verification observe the same notion of "now".
func genAnchor(t *testing.T, name string, validFrom, validUntil time.Time) (Anchor, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return Anchor{
		Name:       name,
		Issuer:     name,
		PublicKey:  &priv.PublicKey,
		ValidFrom:  validFrom,
		ValidUntil: validUntil,
	}, priv
}

// signECDSA computes the ASN.1-encoded ECDSA signature over SHA-256 of
// payload — mirrors the offline CA's sign-flow byte-for-byte (Block 79
// commit 3dd4a765).
func signECDSA(t *testing.T, priv *ecdsa.PrivateKey, payload []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("ecdsa.SignASN1: %v", err)
	}
	return sig
}

// wrongCurveKey returns an ecdsa.PublicKey on the P-384 curve — used to
// drive the curve-check error path.
func wrongCurveKey(t *testing.T) *ecdsa.PublicKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey(P384): %v", err)
	}
	return &priv.PublicKey
}

func TestNewSetRejectsBadAnchors(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	goodPub := &priv.PublicKey

	cases := []struct {
		name    string
		anchor  Anchor
		wantErr string
	}{
		{
			name: "empty name",
			anchor: Anchor{
				PublicKey:  goodPub,
				ValidFrom:  now,
				ValidUntil: now.Add(time.Hour),
			},
			wantErr: "name is empty",
		},
		{
			name: "wrong curve",
			anchor: Anchor{
				Name:       "x",
				PublicKey:  wrongCurveKey(t),
				ValidFrom:  now,
				ValidUntil: now.Add(time.Hour),
			},
			wantErr: "want P-256",
		},
		{
			name: "until before from",
			anchor: Anchor{
				Name:       "x",
				PublicKey:  goodPub,
				ValidFrom:  now,
				ValidUntil: now.Add(-time.Hour),
			},
			wantErr: "valid_until",
		},
		{
			name: "until equals from",
			anchor: Anchor{
				Name:       "x",
				PublicKey:  goodPub,
				ValidFrom:  now,
				ValidUntil: now,
			},
			wantErr: "valid_until",
		},
		{
			name: "no pubkey and no SPKI",
			anchor: Anchor{
				Name:       "x",
				ValidFrom:  now,
				ValidUntil: now.Add(time.Hour),
			},
			wantErr: "PublicKey and SPKI both empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSet(tc.anchor)
			if err == nil {
				t.Fatalf("NewSet succeeded; want error containing %q", tc.wantErr)
			}
			if !contains(err.Error(), tc.wantErr) {
				t.Errorf("NewSet err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a, priv := genAnchor(t, "ca-1", now.Add(-time.Hour), now.Add(time.Hour))
	s, err := NewSet(a)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	payload := []byte("happy-bundle-payload")
	sig := signECDSA(t, priv, payload)

	got, err := s.Verify(sig, payload)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Name != "ca-1" {
		t.Errorf("returned anchor = %q, want ca-1", got.Name)
	}
}

func TestVerifyRotationOverlapWindow(t *testing.T) {
	// Both old + new anchors are active. Verify must return the
	// anchor that actually signed (and not the first one merely
	// tried) so audit logs are correct.
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	old, oldPriv := genAnchor(t, "ca-old", now.Add(-30*24*time.Hour), now.Add(7*24*time.Hour))
	newer, newPriv := genAnchor(t, "ca-new", now.Add(-1*24*time.Hour), now.Add(60*24*time.Hour))

	s, err := NewSet(old, newer)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	payload := []byte("rotation-bundle-payload")

	t.Run("signed by old", func(t *testing.T) {
		sig := signECDSA(t, oldPriv, payload)
		got, err := s.Verify(sig, payload)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if got.Name != "ca-old" {
			t.Errorf("anchor = %q, want ca-old", got.Name)
		}
	})

	t.Run("signed by new", func(t *testing.T) {
		sig := signECDSA(t, newPriv, payload)
		got, err := s.Verify(sig, payload)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if got.Name != "ca-new" {
			t.Errorf("anchor = %q, want ca-new", got.Name)
		}
	})
}

func TestVerifyRefusesExpiredAnchor(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a, priv := genAnchor(t, "ca-expired", now.Add(-90*24*time.Hour), now.Add(-1*24*time.Hour))
	s, err := NewSet(a)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	payload := []byte("expired-bundle-payload")
	sig := signECDSA(t, priv, payload)

	got, err := s.Verify(sig, payload)
	if got != nil {
		t.Fatalf("Verify returned anchor %q, want nil", got.Name)
	}
	if !errors.Is(err, ErrNoActiveAnchors) {
		t.Fatalf("Verify err = %v, want ErrNoActiveAnchors", err)
	}
}

func TestVerifyRefusesNotYetValidAnchor(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a, priv := genAnchor(t, "ca-future", now.Add(7*24*time.Hour), now.Add(60*24*time.Hour))
	s, err := NewSet(a)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	payload := []byte("future-bundle-payload")
	sig := signECDSA(t, priv, payload)

	if _, err := s.Verify(sig, payload); !errors.Is(err, ErrNoActiveAnchors) {
		t.Fatalf("Verify err = %v, want ErrNoActiveAnchors", err)
	}
}

func TestVerifyRotationKeepsActiveWhenOneIsExpired(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	old, oldPriv := genAnchor(t, "ca-old", now.Add(-90*24*time.Hour), now.Add(-1*24*time.Hour))
	newer, newPriv := genAnchor(t, "ca-new", now.Add(-1*24*time.Hour), now.Add(60*24*time.Hour))

	s, err := NewSet(old, newer)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	payload := []byte("mixed-rotation-payload")

	gotNew, err := s.Verify(signECDSA(t, newPriv, payload), payload)
	if err != nil {
		t.Fatalf("Verify(new): %v", err)
	}
	if gotNew.Name != "ca-new" {
		t.Errorf("active signer = %q, want ca-new", gotNew.Name)
	}

	if _, err := s.Verify(signECDSA(t, oldPriv, payload), payload); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Verify(old) err = %v, want ErrUntrusted", err)
	}
}

func TestVerifyRefusesUnknownSigner(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a, _ := genAnchor(t, "ca-1", now.Add(-time.Hour), now.Add(time.Hour))
	s, err := NewSet(a)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	s = s.withClock(fixedClock(now))

	attackerPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	payload := []byte("attacker-bundle-payload")
	sig := signECDSA(t, attackerPriv, payload)

	if _, err := s.Verify(sig, payload); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Verify err = %v, want ErrUntrusted", err)
	}
}

func TestVerifyRefusesEmptySignature(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	a, _ := genAnchor(t, "ca-1", now.Add(-time.Hour), now.Add(time.Hour))
	s, _ := NewSet(a)
	s = s.withClock(fixedClock(now))

	if _, err := s.Verify(nil, []byte("payload")); !errors.Is(err, ErrEmptySignature) {
		t.Fatalf("Verify(nil sig) err = %v, want ErrEmptySignature", err)
	}
	if _, err := s.Verify([]byte{}, []byte("payload")); !errors.Is(err, ErrEmptySignature) {
		t.Fatalf("Verify(empty sig) err = %v, want ErrEmptySignature", err)
	}
}

func TestActiveSorted(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	a1, _ := genAnchor(t, "ca-newer", now.Add(-1*24*time.Hour), now.Add(60*24*time.Hour))
	a2, _ := genAnchor(t, "ca-older", now.Add(-30*24*time.Hour), now.Add(7*24*time.Hour))

	s, err := NewSet(a1, a2)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	got := s.Active(now)
	if len(got) != 2 {
		t.Fatalf("Active len = %d, want 2", len(got))
	}
	if got[0].Name != "ca-older" || got[1].Name != "ca-newer" {
		t.Errorf("Active order = [%s, %s], want [ca-older, ca-newer]", got[0].Name, got[1].Name)
	}
}

func TestDefaultEmbeddedSetParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Default() panicked: %v", r)
		}
	}()
	s := Default()
	if len(s.All()) == 0 {
		t.Fatal("Default() returned empty set; expected at least one embedded anchor")
	}
	for _, a := range s.All() {
		if a.PublicKey == nil {
			t.Errorf("anchor %q: PublicKey nil after NewSet inflation", a.Name)
			continue
		}
		if a.PublicKey.Curve == nil || a.PublicKey.Curve.Params().Name != elliptic.P256().Params().Name {
			got := "nil"
			if a.PublicKey.Curve != nil {
				got = a.PublicKey.Curve.Params().Name
			}
			t.Errorf("anchor %q: curve = %s, want P-256", a.Name, got)
		}
		if a.Name == "" {
			t.Error("embedded anchor with empty name")
		}
		if !a.ValidUntil.After(a.ValidFrom) {
			t.Errorf("anchor %q: valid_until not after valid_from", a.Name)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
