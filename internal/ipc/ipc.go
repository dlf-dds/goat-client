// Package ipc carries the local control channel between the goat-client
// GUI (Fyne, mobile shells) and the goat-clientd daemon.
//
// Wire protocol: JSON-RPC 2.0 framed line-by-line over a Unix domain socket
// (Linux, macOS) or a named pipe (Windows). The transport authenticates the
// peer by uid (Unix: SO_PEERCRED / LOCAL_PEERCRED) and only accepts writes
// from a uid the daemon is configured to trust — by default the uid that
// owns the socket. Read-only methods (getStatus, getDiagnostics) are
// available to any local uid; mutating methods (importBundle, connect,
// disconnect) require the trusted uid.
//
// The method set tracks the design doc + ADR 0840:
//
//   - importBundle(bundleBytes) → Bundle: parse + verify a CBOR bundle and
//     persist it as ~/.goat-client/bundle.cbor. Daemon raises tunnel on
//     next connect.
//   - getStatus() → Status: tunnel state, peer pubkey, last-handshake age,
//     bytes in/out, configured endpoints.
//   - connect(): bring the wg-cp0 tunnel up using the persisted bundle.
//   - disconnect(): take the wg-cp0 tunnel down.
//   - getDiagnostics() → Diagnostics: WG log tail, peer endpoint
//     reachability check, recent state transitions.
//
// The package exposes the wire types (request / reply structs) and a
// transport-agnostic Server interface; the per-OS transport lives in
// transport_unix.go / transport_windows.go.
package ipc

import (
	"context"
	"errors"
	"time"
)

// Method is a JSON-RPC method name. Defined as a typed string so the
// daemon's dispatch table and the GUI client share a single source of
// truth.
type Method string

const (
	MethodImportBundle   Method = "importBundle"
	MethodGetStatus      Method = "getStatus"
	MethodConnect        Method = "connect"
	MethodDisconnect     Method = "disconnect"
	MethodGetDiagnostics Method = "getDiagnostics"
	MethodGetMode        Method = "getMode"
	MethodSetMode        Method = "setMode"

	// v0.2 inner-mesh-direct methods per Block 76N deliverable #5.
	// getStatus already embeds an inner-mesh snapshot; these methods
	// expose narrower entry points the GUI uses without the full
	// StatusReply payload (polling, dedicated panel refresh).
	MethodGetInnerMeshStatus      Method = "getInnerMeshStatus"
	MethodSetInnerMeshProfile     Method = "setInnerMeshProfile"
	MethodEnableInnerMesh         Method = "enableInnerMesh"
	MethodDisableInnerMesh        Method = "disableInnerMesh"
	MethodGetInnerMeshDiagnostics Method = "getInnerMeshDiagnostics"
)

// ImportBundleRequest carries the raw CBOR bundle bytes from the GUI to the
// daemon. The daemon parses + verifies + persists.
type ImportBundleRequest struct {
	BundleBytes []byte `json:"bundleBytes"`
}

// ImportBundleReply summarises the parsed bundle so the GUI can show
// issued-to / site / expires before the user clicks Apply. The daemon only
// returns this once verification has passed.
type ImportBundleReply struct {
	DeviceID       string    `json:"deviceID"`
	Site           string    `json:"site"`
	IssuedAt       time.Time `json:"issuedAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	PeerPubkey     []byte    `json:"peerPubkey"`
	EndpointsCount int       `json:"endpointsCount"`
	HasCPDeviceKey bool      `json:"hasCPDeviceKey"`
}

// TunnelState is the daemon-reported lifecycle state of the wg-cp0 tunnel
// as it appears in the JSON-RPC wire (StatusReply.State). Distinct from
// the GUI-facing State in types.go, which has 4 values; the wire form
// carries the additional "no-bundle" value the GUI maps via
// StatusReply.BundleLoaded.
type TunnelState string

const (
	WireStateNoBundle     TunnelState = "no-bundle"
	WireStateDisconnected TunnelState = "disconnected"
	WireStateConnecting   TunnelState = "connecting"
	WireStateConnected    TunnelState = "connected"
	WireStateError        TunnelState = "error"
)

// StatusReply is what `getStatus` returns. Used by the GUI to drive the
// tray icon (green/amber/red) and the status pane.
//
// v0.2: StatusReply now carries Mode + an optional InnerMesh sub-status.
// In wg-cp0-only mode InnerMesh is nil; in netbird-only mode the outer
// State (formerly the wg-cp0 state) reads as WireStateDisconnected and
// the GUI renders the inner-mesh card instead; in combined mode both
// the outer State + the InnerMesh sub-status are populated and the GUI
// stacks two cards.
type StatusReply struct {
	Mode             string        `json:"mode,omitempty"`
	State            TunnelState   `json:"state"`
	BundleLoaded     bool          `json:"bundleLoaded"`
	DeviceID         string        `json:"deviceID,omitempty"`
	Site             string        `json:"site,omitempty"`
	BundleExpiresAt  time.Time     `json:"bundleExpiresAt,omitempty"`
	PeerPubkey       []byte        `json:"peerPubkey,omitempty"`
	LastHandshake    time.Time     `json:"lastHandshake,omitempty"`
	BytesIn          uint64        `json:"bytesIn"`
	BytesOut         uint64        `json:"bytesOut"`
	ConfiguredEndpoints []string   `json:"configuredEndpoints,omitempty"`
	ErrorMessage     string        `json:"errorMessage,omitempty"`
	InnerMesh        *InnerMeshSnapshot `json:"innerMesh,omitempty"`
}

// InnerMeshSnapshot is the inner-mesh subsystem's status surface. Mirrors
// the wg-cp0 outer fields the GUI already renders, so combined-mode can
// reuse the same status-card shape twice.
type InnerMeshSnapshot struct {
	State         TunnelState `json:"state"`
	PeerCount     int         `json:"peerCount"`
	BytesIn       uint64      `json:"bytesIn"`
	BytesOut      uint64      `json:"bytesOut"`
	LastHandshake time.Time   `json:"lastHandshake,omitempty"`
}

// GetModeReply / SetModeRequest / SetModeReply carry the v0.2 mode
// selector. SetMode rejects unknown modes via ErrUnknownMode.
type GetModeReply struct {
	Mode string `json:"mode"`
}

type SetModeRequest struct {
	Mode string `json:"mode"`
}

type SetModeReply struct {
	// PreviousMode is the mode the daemon was in before the switch — the
	// GUI uses this to log a clean diff and to roll back if the new mode
	// fails to bring its subsystems up.
	PreviousMode string `json:"previousMode"`
	// Mode is the new active mode.
	Mode string `json:"mode"`
}

// SetInnerMeshProfileRequest carries the inner-mesh Config fields the
// daemon hands to Mesh.Configure. ManagementURL + SetupKey are
// required; the optional fields default to empty / nil.
//
// Wire shape: []byte is base64-encoded by encoding/json by default
// (mobileCert, preSharedKey) — that's the round-trip the GUI client
// expects when extracting these fields from a parsed bundle.
type SetInnerMeshProfileRequest struct {
	ManagementURL    string `json:"managementURL"`
	SetupKey         string `json:"setupKey"`
	AdminAccessToken string `json:"adminAccessToken,omitempty"`
	MobileCert       []byte `json:"mobileCert,omitempty"`
	PreSharedKey     []byte `json:"preSharedKey,omitempty"`
}

// InnerMeshDiagnosticsReply carries the inner-mesh log tail + peer
// stats. The log tail is a rolling buffer the daemon keeps for the
// GUI's diagnostics pane; peer stats are per-peer counters
// (populated by the netbird-library-backed impl once it lands — the
// Fake reports the aggregate Stats here too).
type InnerMeshDiagnosticsReply struct {
	LogTail   []string             `json:"logTail"`
	PeerStats []InnerMeshPeerStats `json:"peerStats,omitempty"`
}

// InnerMeshPeerStats is the per-peer counter shape the GUI's
// expanded diagnostics view renders one row per. Fake reports a
// single synthetic entry covering its aggregate Stats; the
// netbird-library impl populates one per real peer.
type InnerMeshPeerStats struct {
	PeerPubKey    string    `json:"peerPubKey"`
	BytesIn       uint64    `json:"bytesIn"`
	BytesOut      uint64    `json:"bytesOut"`
	LastHandshake time.Time `json:"lastHandshake,omitempty"`
}

// EmptyRequest / EmptyReply are placeholders for methods that take or
// return no fields. Kept named (rather than reusing `struct{}`) so the
// JSON-RPC error shape stays stable when fields are added.
type EmptyRequest struct{}
type EmptyReply struct{}

// DiagnosticsReply is what `getDiagnostics` returns. The log buffer is a
// rolling tail (size-bounded by the daemon) so callers always get a
// bounded payload; full WG logs live on the daemon's stderr / journal.
type DiagnosticsReply struct {
	LogTail            []string  `json:"logTail"`
	LastConnectAttempt time.Time `json:"lastConnectAttempt,omitempty"`
	LastError          string    `json:"lastError,omitempty"`
	UptimeSeconds      int64     `json:"uptimeSeconds"`
}

// Handler is the daemon's business-logic interface, separate from the
// transport. The transport (Unix socket / named pipe) parses JSON-RPC
// envelopes and invokes the matching Handler method; tests stub this
// interface directly.
type Handler interface {
	ImportBundle(ctx context.Context, req ImportBundleRequest) (ImportBundleReply, error)
	GetStatus(ctx context.Context) (StatusReply, error)
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	GetDiagnostics(ctx context.Context) (DiagnosticsReply, error)
	GetMode(ctx context.Context) (GetModeReply, error)
	SetMode(ctx context.Context, req SetModeRequest) (SetModeReply, error)

	// v0.2 inner-mesh-direct methods.
	GetInnerMeshStatus(ctx context.Context) (InnerMeshSnapshot, error)
	SetInnerMeshProfile(ctx context.Context, req SetInnerMeshProfileRequest) error
	EnableInnerMesh(ctx context.Context) error
	DisableInnerMesh(ctx context.Context) error
	GetInnerMeshDiagnostics(ctx context.Context) (InnerMeshDiagnosticsReply, error)
}

// PeerCreds carries the OS-level credentials the transport authenticated
// at accept-time. Mutating methods inspect Uid against the daemon's
// configured trusted uid.
type PeerCreds struct {
	Uid uint32
	Pid uint32
}

// ErrUnauthorized is returned to a peer that does not meet the uid
// requirement for a mutating method.
var ErrUnauthorized = errors.New("ipc: peer uid not authorized")

// ErrUnknownMethod is returned for a JSON-RPC request whose method name is
// not in the dispatch table.
var ErrUnknownMethod = errors.New("ipc: unknown method")
