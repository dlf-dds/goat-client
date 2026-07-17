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
	"strings"
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

	// RosenpassEnabled turns on the Rosenpass hybrid post-quantum key
	// exchange on the inner mesh (embed parity with `--enable-rosenpass`).
	// Delivered as per-goatnet policy via the enrollment bundle, so a
	// goat-client device gets PQC the same way standalone-netbird peers
	// do at `netbird up`. See rosenpass-intra-mesh-pqc.md.
	RosenpassEnabled bool

	// RosenpassPermissive lets a Rosenpass-enabled inner-mesh peer still
	// accept plain WireGuard from not-yet-enabled peers (mixed-fleet
	// rollout). Only meaningful when RosenpassEnabled is true.
	RosenpassPermissive bool

	// BundleDeviceID is the operator-assigned device label from the
	// EnrollmentBundle (b.DeviceID). FromBundle populates this; the
	// Mesh implementation composes it with the device-reported deviceID
	// (passed to NewNetbird / NewWithDeviceID) at Connect-time to form
	// embed.Options.DeviceName.
	//
	// The composed name (format: "<BundleDeviceID> (<deviceID>)") flows
	// to netbird mgmt as peer.Meta.Hostname and feeds the {hostname}
	// substitution in the SetupKey's AutoPeerNameTemplate (per
	// dfarrel1/netbird c33517a5). This lets an operator who minted a
	// bundle for "ops-laptop-04" see "ops-laptop-04 (Dene's iPhone)"
	// in the mgmt UI when the device registers — bridging
	// operator-intended identity to device-reported identity for the
	// minting-to-peer matching workflow.
	BundleDeviceID string
}

// Stats is the inner-mesh status snapshot surfaced through the GUI's
// status pane.
type Stats struct {
	PeerCount     int
	BytesIn       uint64
	BytesOut      uint64
	LastHandshake time.Time
	// RosenpassPeers is how many of PeerCount connections have Rosenpass
	// (post-quantum) key exchange active — the "Quantum resistance: true"
	// signal from netbird's per-peer state. 0 = no PQC on any link.
	RosenpassPeers int
}

// PeerStatus is one inner-mesh peer's status, mapped from netbird's
// per-peer FullStatus. It is the source for the connectivity-check
// panel's identity fields and its direct-vs-relayed badge (ADR 1063 in
// the DesertBreadBird core repo).
//
// The live latency graph does NOT come from Latency here: that is
// netbird's one-shot ICE reading, frozen at candidate-pair selection and
// zero on the relay path. Live RTT comes from internal/peerping, keyed to
// IP — which is both this peer's overlay address and what peerping probes.
type PeerStatus struct {
	IP            string        // NetBird overlay (tunnel) IP — peerping's target
	PubKey        string        // WireGuard public key (stable identity)
	FQDN          string        // mesh DNS name
	Connected     bool          // ConnStatus == Connected
	Relayed       bool          // true = via a relay (TURN or netbird relay); false = direct P2P
	LocalICEType  string        // host / srflx / relay / prflx
	RemoteICEType string        // as above, for the remote candidate
	RelayAddress  string        // set on the netbird-relay path
	LastHandshake time.Time     // last WireGuard handshake
	BytesRx       uint64        // cumulative WG receive counter
	BytesTx       uint64        // cumulative WG transmit counter
	Latency       time.Duration // netbird's stale one-shot ICE RTT; prefer peerping for live
}

// Path returns the direct-vs-relayed label for the connectivity-check
// badge: "relayed" when the selected path runs through a relay, else
// "direct". Only meaningful when Connected.
func (p PeerStatus) Path() string {
	if p.Relayed {
		return "relayed"
	}
	return "direct"
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

	// Peers returns per-peer status for every inner-mesh peer, mapped
	// from netbird's FullStatus. Returns an empty slice (not an error)
	// when the mesh is not up. This is the source for the
	// connectivity-check panel's direct/relayed badge + identity fields;
	// the live latency graph comes from internal/peerping, not from
	// PeerStatus.Latency. Additive extension to the v0.2 interface.
	Peers() ([]PeerStatus, error)

	// LocalIP returns this node's own overlay (tunnel) IP, or "" when the
	// mesh is not up / the IP is not yet known. The peerping subsystem
	// binds its echo Responder to this address to keep it mesh-only.
	LocalIP() (string, error)

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
// Callers with an optional caller-supplied device name (notably the
// mobile SDKs, where the native shell hands a UIDevice.current.name /
// Build.MODEL string in) should reach for NewWithDeviceID instead —
// it falls back to New when the supplied ID is empty.
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

// NewWithDeviceID returns the canonical inner-mesh implementation,
// using deviceID as the *device-reported half* of the peer identity
// netbird's management plane sees. Empty deviceID falls through to
// New() — useful for callers with an *optional* caller-supplied name
// who want the package-level default when the caller's name is unset.
//
// Identity is composed at Connect-time from two halves:
//
//   - bundle half — `Config.BundleDeviceID`, populated by FromBundle
//     from the EnrollmentBundle's operator-set DeviceID. Identifies
//     *which minting* the registering peer corresponds to (load-
//     bearing for matching minted bundle → registered peer).
//
//   - device half — this argument. Identifies what the device knows
//     itself as. On iOS that's typically UIDevice.current.name (iOS
//     16+ generalises to "iPhone" without the user-assigned-device-
//     name entitlement); on Android that's "Build.MANUFACTURER
//     Build.MODEL"; on desktop/headless that's the host's hostname.
//
// The composed string `"<bundle> (<device>)"` flows to
// embed.Options.DeviceName and arrives server-side as
// peer.Meta.Hostname — the `{hostname}` substitution in the
// SetupKey's AutoPeerNameTemplate (per dfarrel1/netbird c33517a5).
// An operator minting a SetupKey with an empty template still gets
// both pieces of identity in the default peer name.
//
// Mobile SDK call sites (mobile/{ios,android}/GoatClientSDK) reach
// this from the Run() path with `c.deviceName` already populated.
// The BundleCapabilities probe constructs an ephemeral client with
// empty deviceName; that path never Connects, so the empty-fallback
// behaviour is benign.
func NewWithDeviceID(deviceID string) Mesh {
	if deviceID == "" {
		return New()
	}
	return NewNetbird(deviceID)
}

// composeIdentity composes the operator-assigned bundle DeviceID with
// the device-reported deviceID into the string used as
// embed.Options.DeviceName. Format: "<bundleDeviceID> (<deviceID>)"
// when both are non-empty; either alone when one is empty; "" when
// both are empty (embed.New would reject — caller's responsibility
// to ensure at least one half is set when Connecting).
//
// Whitespace is trimmed because some platform identity sources
// (UIDevice.current.name, Build.MODEL) commonly leak surrounding
// spaces.
func composeIdentity(bundleDeviceID, deviceID string) string {
	bundleDeviceID = strings.TrimSpace(bundleDeviceID)
	deviceID = strings.TrimSpace(deviceID)
	switch {
	case bundleDeviceID != "" && deviceID != "":
		return bundleDeviceID + " (" + deviceID + ")"
	case bundleDeviceID != "":
		return bundleDeviceID
	default:
		return deviceID
	}
}
