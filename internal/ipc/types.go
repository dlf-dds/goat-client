// Package ipc defines the wire types and client interface the desktop GUI
// uses to talk to goat-clientd. Transport is JSON-RPC over a Unix domain
// socket on Linux/macOS and a named pipe on Windows; that wiring is owned
// by Track A. This package owns the contract.
package ipc

import (
	"context"
	"time"
)

// State enumerates the high-level tunnel states the GUI surfaces.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateError        State = "error"
)

// StatusInfo is the snapshot the GUI renders on the status pane and uses to
// pick the systray icon color. All fields are zero-valued when the daemon
// has no bundle imported or no tunnel up.
//
// v0.2: Mode + InnerMesh fields carry the multi-subsystem shape. State /
// InterfaceName / etc. continue to describe the wg-cp0 outer tunnel;
// InnerMesh describes the inner-mesh leg when present.
type StatusInfo struct {
	Mode           string       `json:"mode,omitempty"`
	State          State        `json:"state"`
	InterfaceName  string       `json:"interface_name,omitempty"`
	LastHandshake  time.Time    `json:"last_handshake,omitempty"`
	BytesIn        uint64       `json:"bytes_in,omitempty"`
	BytesOut       uint64       `json:"bytes_out,omitempty"`
	PeerPubKey     string       `json:"peer_pubkey,omitempty"`
	Endpoints      []string     `json:"endpoints,omitempty"`
	BundleImported bool         `json:"bundle_imported"`
	Bundle         *BundleInfo  `json:"bundle,omitempty"`
	ErrorMessage   string       `json:"error_message,omitempty"`
	InnerMesh      *InnerMeshInfo `json:"inner_mesh,omitempty"`
}

// InnerMeshInfo mirrors InnerMeshSnapshot for the GUI side.
type InnerMeshInfo struct {
	State         State     `json:"state"`
	PeerCount     int       `json:"peer_count"`
	BytesIn       uint64    `json:"bytes_in"`
	BytesOut      uint64    `json:"bytes_out"`
	LastHandshake time.Time `json:"last_handshake,omitempty"`
}

// BundleInfo summarises an imported bundle for display in the GUI.
//
// Mirrors the user-visible subset of the offline-CA-signed CBOR bundle:
// see docs/design/offline-enrollment.md in goat-trunk for the full schema.
type BundleInfo struct {
	IssuedTo   string    `json:"issued_to"`
	Site       string    `json:"site"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	PeerPubKey string    `json:"peer_pubkey"`
	Endpoints  []string  `json:"endpoints"`
}

// Diagnostics carries the WireGuard log tail + last reachability probe
// result, surfaced by the GUI's Diagnostics tab.
type Diagnostics struct {
	LogTail    []string  `json:"log_tail"`
	LastProbe  time.Time `json:"last_probe,omitempty"`
	Reachable  bool      `json:"reachable"`
	ProbeError string    `json:"probe_error,omitempty"`
}

// Client is the GUI's view of the daemon. Implementations may be a real
// JSON-RPC client (Track A) or the in-process stub used for GUI development
// before the daemon converges.
type Client interface {
	// ImportBundle hands raw CBOR bytes to the daemon, which parses,
	// signature-verifies against the pinned offline-CA root, and persists
	// the result. The returned BundleInfo is the parsed summary the GUI
	// shows in its Bundle tab.
	ImportBundle(ctx context.Context, raw []byte) (*BundleInfo, error)

	// GetStatus returns the current tunnel snapshot.
	GetStatus(ctx context.Context) (*StatusInfo, error)

	// Connect asks the daemon to bring the wg-cp0 tunnel up. Returns
	// promptly; the actual state transition is observed via GetStatus.
	Connect(ctx context.Context) error

	// Disconnect asks the daemon to bring the tunnel down.
	Disconnect(ctx context.Context) error

	// GetDiagnostics returns log tail + last reachability probe info.
	GetDiagnostics(ctx context.Context) (*Diagnostics, error)

	// GetMode returns the current v0.2 mode (wg-cp0-only / netbird-only /
	// combined). The GUI uses this to seed the Settings → Mode panel
	// default selection.
	GetMode(ctx context.Context) (string, error)

	// SetMode asks the daemon to switch to a new mode. The daemon tears
	// down the active subsystems for the previous mode and brings up the
	// new mode's subsystems; the GUI shows a "Reconnecting tunnels…"
	// dialog until GetStatus reports the new active state. Returns the
	// previous mode for diff / rollback purposes.
	SetMode(ctx context.Context, mode string) (previous string, err error)

	// Close releases any underlying transport resources.
	Close() error
}
