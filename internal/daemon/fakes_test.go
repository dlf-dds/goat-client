package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/tunnel"
)

// fakeTunnel implements TunnelManager without opening a TUN device.
// Records call counts so the smoke test can assert which legs were
// touched per mode. State transitions mirror tunnel.Manager: Configure
// is a no-op on cfg; Connect promotes to StateUp; Disconnect returns
// to StateClosed.
type fakeTunnel struct {
	mu             sync.Mutex
	state          tunnel.State
	cfg            tunnel.Config
	configureCalls int
	connectCalls   int
	disconnectCalls int
	closeCalls     int
}

func newFakeTunnel() *fakeTunnel {
	return &fakeTunnel{state: tunnel.StateClosed}
}

func (f *fakeTunnel) Configure(cfg tunnel.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	f.configureCalls++
	return nil
}

func (f *fakeTunnel) Connect(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	f.state = tunnel.StateUp
	return nil
}

func (f *fakeTunnel) Disconnect(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls++
	f.state = tunnel.StateClosed
	return nil
}

func (f *fakeTunnel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	f.state = tunnel.StateClosed
	return nil
}

func (f *fakeTunnel) State() tunnel.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeTunnel) Stats() (tunnel.Stats, error) {
	return tunnel.Stats{}, nil
}

// Compile-time assertion: fakeTunnel satisfies the daemon's contract.
var _ TunnelManager = (*fakeTunnel)(nil)

// testBundle pairs a signed CBOR-marshalled EnrollmentBundle with the
// TrustRoots configured to accept its signature. The fields the smoke
// test wires through to the daemon's flows (wg-cp0 keypair + address
// for HasWgCp0; InnerMeshSetup for HasInnerMesh) are populated per
// the caller's request.
type testBundle struct {
	parsed *bundle.EnrollmentBundle
	bytes  []byte
	roots  *bundle.TrustRoots
}

// mintTestBundle builds a signed EnrollmentBundle exercising the
// requested capability bits, signs it with a fresh ECDSA P-256 key,
// and returns the parsed bundle, wire bytes, and matching TrustRoots.
//
// withWgCp0 populates the cp_device_* fields + a relay endpoint so
// tunnel.FromBundle + Manager.Configure succeed.
//
// withInnerMesh populates InnerMeshSetup with the supplied managementURL
// (typically "http://" + fakemgmt.Addr()) so innermesh.FromBundle returns
// a usable Config.
func mintTestBundle(t *testing.T, withWgCp0 bool, innerMeshMgmtURL string) testBundle {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa generate: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	b := &bundle.EnrollmentBundle{
		Version:            bundle.Version,
		DeviceID:           "three-mode-smoke-device",
		PeerPubkey:         repeatByte('p', 32),
		ACLGroups:          []string{"three-mode-smoke"},
		Site:               "three-mode-smoke-lab",
		IssuedAt:           now.Add(-1 * time.Hour),
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(24 * time.Hour),
		Nonce:              repeatByte('n', 16),
		CAID:               "three-mode-smoke-ca",
	}

	if withWgCp0 {
		// CheckCPDeviceKeypair verifies *paired* presence, not curve
		// agreement, so deterministic byte fills are sufficient for the
		// in-process smoke. The fake tunnel never touches these.
		b.CPDevicePubkey = repeatByte('u', 32)
		b.CPDevicePrivkey = repeatByte('v', 32)
		b.CPDeviceAddress = "198.18.0.42/24"
		b.KnownEndpoints = []bundle.KnownEndpoint{{
			Addr:     "127.0.0.1:51820",
			Pubkey:   repeatByte('r', 32),
			Kind:     bundle.KindRelay,
			MeshAddr: "198.18.0.10",
		}}
	}

	if innerMeshMgmtURL != "" {
		b.InnerMeshSetup = bundle.InnerMeshSetup{
			ManagementURL: innerMeshMgmtURL,
			// embed.New requires a non-empty SetupKey; auth.registerPeer
			// uuid-parses it, but the fakemgmt skips registration so any
			// non-empty value suffices.
			SetupKey: "11111111-1111-1111-1111-111111111111",
		}
	}

	payload, err := b.Signable()
	if err != nil {
		t.Fatalf("signable: %v", err)
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	b.Signature = sig

	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	roots, err := bundle.NewTrustRoots(&priv.PublicKey)
	if err != nil {
		t.Fatalf("trust roots: %v", err)
	}
	return testBundle{parsed: b, bytes: wire, roots: roots}
}

func repeatByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// mintTestBundleWithRoots mints a fresh bundle whose key is added to
// the supplied TrustRoots set. Lets multi-profile tests build N
// bundles that all verify against the same Store-configured roots.
//
// The minted bundle uses a randomised DeviceID + Nonce so it doesn't
// wire-collide with sibling bundles in the same test.
func mintTestBundleWithRoots(t *testing.T, withWgCp0 bool, innerMeshMgmtURL string, roots *bundle.TrustRoots) testBundle {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa generate: %v", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	deviceID := "multi-profile-device-" + string([]byte{
		hexChar(nonce[0]), hexChar(nonce[1] >> 4), hexChar(nonce[2]), hexChar(nonce[3] >> 4),
	})
	now := time.Now().UTC().Truncate(time.Second)
	b := &bundle.EnrollmentBundle{
		Version:            bundle.Version,
		DeviceID:           deviceID,
		PeerPubkey:         repeatByte('p', 32),
		ACLGroups:          []string{"multi-profile-smoke"},
		Site:               "multi-profile-lab-" + string([]byte{hexChar(nonce[4])}),
		IssuedAt:           now.Add(-1 * time.Hour),
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(24 * time.Hour),
		Nonce:              nonce,
		CAID:               "multi-profile-ca",
	}
	if withWgCp0 {
		b.CPDevicePubkey = repeatByte('u', 32)
		b.CPDevicePrivkey = repeatByte('v', 32)
		b.CPDeviceAddress = "198.18.0.42/24"
		b.KnownEndpoints = []bundle.KnownEndpoint{{
			Addr:     "127.0.0.1:51820",
			Pubkey:   repeatByte('r', 32),
			Kind:     bundle.KindRelay,
			MeshAddr: "198.18.0.10",
		}}
	}
	if innerMeshMgmtURL != "" {
		b.InnerMeshSetup = bundle.InnerMeshSetup{
			ManagementURL: innerMeshMgmtURL,
			SetupKey:      "11111111-1111-1111-1111-111111111111",
		}
	}
	payload, err := b.Signable()
	if err != nil {
		t.Fatalf("signable: %v", err)
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	b.Signature = sig
	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := roots.Add(&priv.PublicKey); err != nil {
		t.Fatalf("trust roots add: %v", err)
	}
	return testBundle{parsed: b, bytes: wire, roots: roots}
}

func hexChar(b byte) byte {
	const hex = "0123456789abcdef"
	return hex[b&0xF]
}
