package innermesh

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dlf-dds/goat-client/internal/bundle"
)

// Compile-time assertion: Fake implements Mesh (catches the Logs()
// method addition if any future change reshapes the interface).
var _ Mesh = (*Fake)(nil)

func TestFromBundleNilBundle(t *testing.T) {
	t.Parallel()
	if _, err := FromBundle(nil); err == nil {
		t.Fatal("FromBundle(nil): want error, got nil")
	}
}

func TestFromBundleNoInnerMeshSetup(t *testing.T) {
	t.Parallel()
	b := &bundle.EnrollmentBundle{Version: bundle.Version}
	_, err := FromBundle(b)
	if !errors.Is(err, ErrBundleMissingInnerMeshSetup) {
		t.Fatalf("FromBundle: got %v, want ErrBundleMissingInnerMeshSetup", err)
	}
}

func TestFromBundleHappyPath(t *testing.T) {
	t.Parallel()
	psk := make([]byte, 32)
	for i := range psk {
		psk[i] = byte(i)
	}
	cert := []byte("-----BEGIN CERTIFICATE-----\nstub\n-----END CERTIFICATE-----\n")
	b := &bundle.EnrollmentBundle{
		Version: bundle.Version,
		InnerMeshSetup: bundle.InnerMeshSetup{
			ManagementURL:    "https://mgmt.example.internal:33073",
			SetupKey:         "TEST-SETUP-KEY",
			AdminAccessToken: "admin-tok",
			PreSharedKey:     psk,
		},
		MobileCert: cert,
	}
	c, err := FromBundle(b)
	if err != nil {
		t.Fatalf("FromBundle: %v", err)
	}
	if c.ManagementURL != b.InnerMeshSetup.ManagementURL {
		t.Errorf("ManagementURL: got %q, want %q", c.ManagementURL, b.InnerMeshSetup.ManagementURL)
	}
	if c.SetupKey != b.InnerMeshSetup.SetupKey {
		t.Errorf("SetupKey: got %q, want %q", c.SetupKey, b.InnerMeshSetup.SetupKey)
	}
	if c.AdminAccessToken != b.InnerMeshSetup.AdminAccessToken {
		t.Errorf("AdminAccessToken: got %q, want %q", c.AdminAccessToken, b.InnerMeshSetup.AdminAccessToken)
	}
	if !bytes.Equal(c.PreSharedKey, psk) {
		t.Errorf("PreSharedKey: got %x, want %x", c.PreSharedKey, psk)
	}
	if !bytes.Equal(c.MobileCert, cert) {
		t.Errorf("MobileCert: got %d bytes, want %d bytes", len(c.MobileCert), len(cert))
	}
	// Defensive-copy check: mutating Config bytes does not mutate
	// the source bundle.
	if len(c.PreSharedKey) > 0 {
		c.PreSharedKey[0] ^= 0xff
		if bytes.Equal(c.PreSharedKey, b.InnerMeshSetup.PreSharedKey) {
			t.Errorf("FromBundle did not defensive-copy PreSharedKey (mutation aliased)")
		}
	}
}

func TestFakeLogsRingBuffer(t *testing.T) {
	t.Parallel()
	f := NewFake()
	if err := f.Configure(Config{
		ManagementURL: "https://m.example",
		SetupKey:      "k",
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	_ = f.Connect(context.Background())
	_ = f.Disconnect(context.Background())
	_ = f.Close()

	all := f.Logs(0)
	if len(all) < 3 {
		t.Errorf("expected at least 3 log lines, got %d", len(all))
	}
	tail := f.Logs(2)
	if len(tail) != 2 {
		t.Errorf("Logs tail=2 len: got %d, want 2", len(tail))
	}
	// tail larger than buffer returns the whole buffer.
	huge := f.Logs(100)
	if len(huge) != len(all) {
		t.Errorf("Logs tail=100 len: got %d, want %d", len(huge), len(all))
	}
}
