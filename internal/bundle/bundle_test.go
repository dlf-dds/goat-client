package bundle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

func newTestBundle(t *testing.T) (*EnrollmentBundle, *ecdsa.PrivateKey, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa P-256: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	b := &EnrollmentBundle{
		Version:            Version,
		DeviceID:           "test-device",
		PeerPubkey:         []byte("0123456789abcdef0123456789abcdef"),
		ACLGroups:          []string{"workstations"},
		Site:               "kwt-aj-A",
		KnownEndpoints:     []KnownEndpoint{{Addr: "10.0.0.1:51820", Pubkey: []byte("relaypubkey00000000000000000000aa"), Kind: KindRelay, MeshAddr: "198.18.0.1"}},
		IssuedAt:           now,
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(365 * 24 * time.Hour),
		Nonce:              []byte("nonce-bytes-1234"),
		CAID:               "offline-ca-ecdsa-2026-05",
	}
	if err := b.Sign(func(payload []byte) ([]byte, error) {
		digest := sha256.Sum256(payload)
		return ecdsa.SignASN1(rand.Reader, priv, digest[:])
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return b, priv, &priv.PublicKey
}

// Sign produces an ECDSA P-256 ASN.1-encoded signature over SHA-256 of
// the canonical payload and stores it on the bundle. Test-only helper —
// production signing happens in the offline-CA host workflow
// (goat-trunk ops/enrollment), not in the daemon.
func (b *EnrollmentBundle) Sign(sign func([]byte) ([]byte, error)) error {
	payload, err := b.Signable()
	if err != nil {
		return err
	}
	sig, err := sign(payload)
	if err != nil {
		return err
	}
	if len(sig) == 0 {
		return errors.New("signature is empty")
	}
	b.Signature = sig
	return nil
}

func TestRoundTripVerify(t *testing.T) {
	b, _, pub := newTestBundle(t)
	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Unmarshal(wire)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DeviceID != b.DeviceID {
		t.Errorf("DeviceID round-trip: got %q want %q", got.DeviceID, b.DeviceID)
	}
	if err := got.Verify(pub); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	b, _, pub := newTestBundle(t)
	b.DeviceID = "evil-device"
	if err := b.Verify(pub); err == nil {
		t.Fatal("Verify accepted tampered bundle")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	b, _, _ := newTestBundle(t)
	otherPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}
	if err := b.Verify(&otherPriv.PublicKey); err == nil {
		t.Fatal("Verify accepted bundle with wrong key")
	}
}

func TestVerifyRejectsWrongCurve(t *testing.T) {
	b, _, _ := newTestBundle(t)
	otherPriv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa P384: %v", err)
	}
	err = b.Verify(&otherPriv.PublicKey)
	if err == nil {
		t.Fatal("Verify accepted non-P-256 key")
	}
	if !contains(err.Error(), "P-256") {
		t.Errorf("Verify err = %v, want curve-mismatch error", err)
	}
}

func TestVerifyRejectsNilKey(t *testing.T) {
	b, _, _ := newTestBundle(t)
	if err := b.Verify(nil); err == nil {
		t.Fatal("Verify accepted nil key")
	}
}

func TestCheckExpiry(t *testing.T) {
	b := &EnrollmentBundle{ExpiresAt: time.Now().Add(-time.Hour)}
	if err := b.CheckExpiry(time.Now()); !errors.Is(err, ErrExpired) {
		t.Errorf("CheckExpiry: want ErrExpired, got %v", err)
	}
	b.ExpiresAt = time.Now().Add(time.Hour)
	if err := b.CheckExpiry(time.Now()); err != nil {
		t.Errorf("CheckExpiry on fresh bundle: %v", err)
	}
}

func TestCheckCPDeviceKeypairUnpaired(t *testing.T) {
	b := &EnrollmentBundle{CPDevicePubkey: []byte("only-pub")}
	if err := b.CheckCPDeviceKeypair(); !errors.Is(err, ErrCPDeviceKeypairUnpaired) {
		t.Errorf("CheckCPDeviceKeypair: want ErrCPDeviceKeypairUnpaired, got %v", err)
	}
}

func TestTrustRootsVerify(t *testing.T) {
	b, _, pub := newTestBundle(t)
	tr, err := NewTrustRoots(pub)
	if err != nil {
		t.Fatalf("NewTrustRoots: %v", err)
	}
	if err := tr.VerifyBundle(b); err != nil {
		t.Errorf("VerifyBundle (matching root): %v", err)
	}
	otherPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}
	tr2, _ := NewTrustRoots(&otherPriv.PublicKey)
	if err := tr2.VerifyBundle(b); !errors.Is(err, ErrUntrustedBundle) {
		t.Errorf("VerifyBundle (non-matching root): want ErrUntrustedBundle, got %v", err)
	}
	empty := &TrustRoots{}
	if err := empty.VerifyBundle(b); !errors.Is(err, ErrNoTrustRoots) {
		t.Errorf("VerifyBundle (empty): want ErrNoTrustRoots, got %v", err)
	}
}

func TestLoadTrustRootsFromPEMPublicKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	tr, err := LoadTrustRootsFromPEM(pemBytes)
	if err != nil {
		t.Fatalf("LoadTrustRootsFromPEM: %v", err)
	}
	if tr.Empty() {
		t.Fatal("trust roots empty after load")
	}
}

func TestLoadTrustRootsFromPEMCertificate(t *testing.T) {
	// Mirror the goat-trunk pattern: the same root key wrapped as an
	// X.509 CERTIFICATE PEM (the Traefik chain anchor at
	// ops/enrollment/ca/dev/root-cert.pem) should also load
	// successfully so operators can point at either file shape without
	// the "expected PUBLIC KEY, got CERTIFICATE" footgun.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa: %v", err)
	}
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: "Test CA"},
		Issuer:                pkix.Name{CommonName: "Test CA"},
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tr, err := LoadTrustRootsFromPEM(pemBytes)
	if err != nil {
		t.Fatalf("LoadTrustRootsFromPEM (CERTIFICATE): %v", err)
	}
	if tr.Empty() {
		t.Fatal("trust roots empty after load")
	}
}

func TestLoadTrustRootsRejectsWrongCurve(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa P384: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	_, err = LoadTrustRootsFromPEM(pemBytes)
	if err == nil {
		t.Fatal("LoadTrustRootsFromPEM accepted P-384 key; want P-256-only")
	}
	if !contains(err.Error(), "P-256") {
		t.Errorf("err = %v, want curve-mismatch error", err)
	}
}

// contains is a local substring helper (mirrors the one in
// trustanchor/anchor_test.go).
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
