// Package innermesh is the v0.2 inner-mesh tunnel subsystem (the
// netbird-derived layer that runs inside goat-client when the operator
// selects `netbird-only` or `combined` mode).
//
// THIS FILE IS A SCAFFOLDING SHIM. The real implementation lands as
// Block 76N (Worker A). See INTERFACE.md in this directory for the
// drafted contract. Until Worker A publishes, the daemon binds to the
// Fake exported below — which is enough to drive the desktop GUI's
// mode-selector + status-pane + tray-icon surface and the headless
// daemon's mode-reconciliation logic end-to-end.
//
// Replace this file (keep INTERFACE.md, replace fake.go) when 76N lands.
package innermesh

import (
	"context"
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

// New constructs the canonical inner-mesh implementation. Until Worker
// A's Block 76N lands this returns the Fake.
func New() Mesh { return NewFake() }
