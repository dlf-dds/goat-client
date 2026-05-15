// Package innermesh is the v0.2 inner-mesh tunnel subsystem (the
// netbird-derived layer that runs inside goat-client when the operator
// selects `netbird-only` or `combined` mode).
//
// Milestones (per UNSTRIP.md): M0+M1 — netbird-library un-strip + cross-
// platform compile (PR #41). M2 — in-process fakemgmt + fakesignal +
// Netbird lifecycle test (PR #43). M3 — Stats/Logs during a real
// session: trivially satisfied because embed.Options.LogOutput populates
// the ring buffer with netbird's logrus output and client.Status()
// drives Stats. M4 — three headless smokes
// (internal/daemon/three_mode_smoke_test.go). M5 — this commit; New()
// now returns *Netbird (deviceID = host hostname with a "goat-client"
// fallback) so goat-clientd + the mobile SDKs run the real un-stripped
// inner mesh by default. Callers that want a platform-specific device
// name (Android Build.MODEL, iOS UIDevice.current.name) construct
// *Netbird directly via NewNetbird(deviceID); tests inject *Fake via
// daemon.Config.InnerMeshFactory.
package innermesh

import (
	"context"
	"os"
	"time"
)

// State mirrors internal/tunnel.State for the inner-mesh subsystem so
// the daemon can render both legs of `combined` mode through a single
// status-pane shape.
type State int

const (
	StateClosed State = iota
	StateConfiguring
	StateUp
	StateError
)

func (s State) String() string {
	switch s {
	case StateConfiguring:
		return "configuring"
	case StateUp:
		return "up"
	case StateError:
		return "error"
	}
	return "closed"
}

// Config carries the inner-mesh setup data. The wire-side source of
// truth is the EnrollmentBundle's inner_mesh_setup field (plus the
// top-level mobile_cert field); FromBundle derives a Config from a
// verified bundle.
type Config struct {
	// SetupKey is the netbird-style join token. v0.2's bundle carries
	// this alongside the wg-cp0 fields.
	SetupKey string

	// ManagementURL points at the inner-mesh management plane. For
	// `combined` mode this is reached via the wg-cp0 outer tunnel.
	// For `netbird-only` mode this is the Block 80 / ADR 0843 public
	// mTLS crutch tier.
	ManagementURL string

	// PreferKernelWG hints whether to use the kernel WG driver vs the
	// embedded wireguard-go. The daemon picks this per-platform.
	// Block 76N implementation enforces userspace-only regardless —
	// mobile builds require it, and desktop pays a small throughput
	// cost vs stock netbird for the library posture.
	PreferKernelWG bool

	// AdminAccessToken is the optional bearer token for the netbird
	// management API. Used by getInnerMeshDiagnostics for richer
	// status (mgmt-API read-only calls). Empty means "no admin
	// token; render the limited status surface."
	AdminAccessToken string

	// MobileCert is the Block 80F per-device mTLS client cert + key
	// bundle (PEM, leaf first then any intermediate chain then the
	// private key block). Required when ManagementURL targets the
	// Block 80 public-mTLS crutch tier; empty for direct mgmt reach.
	MobileCert []byte

	// PreSharedKey is the optional netbird WireGuard pre-shared key
	// passed through to the userspace WG device. 32 bytes when set;
	// empty means "no PSK." Defense-in-depth, not load-bearing.
	PreSharedKey []byte
}

// Stats is the inner-mesh status snapshot surfaced through the GUI's
// status pane.
type Stats struct {
	PeerCount     int
	BytesIn       uint64
	BytesOut      uint64
	LastHandshake time.Time
}

// Mesh is the daemon-visible interface. The real implementation is
// Worker A's; until it lands the daemon binds to NewFake().
type Mesh interface {
	// Configure stores or replaces the desired inner-mesh setup. Safe
	// to call before or after Connect; calling while Up requires the
	// caller to Disconnect + Connect for the new config to take effect.
	Configure(cfg Config) error

	// Connect brings the inner-mesh up. Returns when the mesh has
	// reported success (StateUp) or an error.
	Connect(ctx context.Context) error

	// Disconnect tears the inner-mesh down. Idempotent.
	Disconnect(ctx context.Context) error

	// State returns the current lifecycle state.
	State() State

	// Stats returns the latest snapshot. May return zero values if the
	// mesh is not StateUp.
	Stats() (Stats, error)

	// Logs returns the most recent tail log lines from the inner
	// mesh's internal log buffer. tail <= 0 returns the entire
	// buffer (bounded by the implementation's ring-buffer capacity).
	// Used by the diagnostics IPC method
	// (getInnerMeshDiagnostics) and the GUI Diagnostics pane — not
	// by hot-path code. Added under Block 76N as an additive
	// extension to the v0.2 placeholder interface.
	Logs(tail int) []string

	// Close releases any underlying resources. After Close the Mesh
	// must not be reused.
	Close() error
}

// New constructs the canonical inner-mesh implementation — the real
// netbird-backed Mesh (NewNetbird). DeviceID is the host's os.Hostname,
// falling back to "goat-client" when the hostname is unreadable or
// empty. Side-effect-free: the embed.Client is built lazily at Connect.
//
// Tests that need a deterministic in-memory mesh inject *Fake via
// daemon.Config.InnerMeshFactory (or use NewFake() directly).
func New() Mesh { return NewNetbird(defaultDeviceID()) }

// defaultDeviceID returns the host's hostname for the deviceID netbird
// uses to register with the management plane. Falls back to
// "goat-client" when os.Hostname errs or returns an empty string.
func defaultDeviceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "goat-client"
	}
	return host
}
