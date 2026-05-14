//go:build ios

package GoatClientSDK

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/trustanchor"
	"github.com/dlf-dds/goat-client/internal/tunnel"
)

// ErrNoBundleImported is returned by Run() if ImportBundle hasn't yet been
// called successfully and no persisted bundle is on disk. The bundle carries
// the wg-cp0 peer pubkey + endpoints + assigned tunnel address; without it
// the SDK has no config to apply.
var ErrNoBundleImported = errors.New("goat-client iOS SDK: no bundle imported (call ImportBundle first)")

// TunnelState mirrors the (very narrow) tunnel status surface exposed to
// Swift. Single peer (wg-cp0), so no peer list — just the one connection.
const (
	StateDisconnected = "disconnected"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateError        = "error"
)

// Client is the gomobile-bound facade the Swift NEPacketTunnelProvider
// instantiates. Lifecycle: NewClient -> ImportBundle (once per fresh
// install or rotation) -> Run (blocking; called from the NE extension's
// startTunnel) -> Stop (called from stopTunnel).
//
// Heavily reshaped from netbird's NetBirdSDK.Client — login/OAuth machinery
// is gone (single-tunnel offline-bundle onboarding model; see
// docs/design/goat-client.md §"Bundle import vs OAuth login"), and per-peer
// mesh status is collapsed to a single peer state.
type Client struct {
	cfgDir                string // App Group container path (writable by both app + NE extension)
	stateFile             string // persistent tunnel state JSON path within cfgDir
	deviceName            string
	osName                string
	osVersion             string
	networkChangeListener NetworkChangeListener
	dnsManager            DnsManager

	mu          sync.Mutex
	mode        string // v0.2 operating mode: wg-cp0-only / netbird-only / combined
	innerMesh   innermesh.Mesh // populated when mode includes inner mesh; nil otherwise
	ctxCancel   context.CancelFunc
	stateAtomic atomic.Value // string — current StateXxx
}

// NewClient is gomobile-callable. cfgDir is typically the App Group container
// shared between the main app (which calls ImportBundle) and the NE extension
// (which calls Run). stateFile is a JSON file the engine reads/writes for
// last-handshake / bytes counters.
//
// networkChangeListener and dnsManager are Swift-implemented (see listeners.go).
// Both may be nil for unit-style smoke tests; production callers should wire
// real implementations in the NEPacketTunnelProvider.
func NewClient(
	cfgDir string,
	stateFile string,
	deviceName string,
	osVersion string,
	osName string,
	networkChangeListener NetworkChangeListener,
	dnsManager DnsManager,
) *Client {
	c := &Client{
		cfgDir:                cfgDir,
		stateFile:             stateFile,
		deviceName:            deviceName,
		osName:                osName,
		osVersion:             osVersion,
		networkChangeListener: networkChangeListener,
		dnsManager:            dnsManager,
	}
	c.stateAtomic.Store(StateDisconnected)
	return c
}

// ImportBundle takes the raw bytes of an offline-CA-signed CBOR bundle (see
// docs/design/offline-enrollment.md and goat-trunk's
// ops/enrollment/cmd/bundle-extract for the format), parses it, verifies the
// Ed25519 signature against the build-time-pinned trust anchors
// (internal/trustanchor.Default), and persists the raw bytes under cfgDir for
// Run() to re-load.
//
// gomobile cannot bind a Go []byte parameter directly — it gets bridged to
// Swift Data / NSData. Swift callers pass the raw bundle file contents read
// via UIDocumentPicker (file-picker import) or AVFoundation (QR scan + base64
// decode).
func (c *Client) ImportBundle(bundleBytes []byte) error {
	if len(bundleBytes) == 0 {
		return fmt.Errorf("empty bundle")
	}
	if c.cfgDir == "" {
		return fmt.Errorf("client cfgDir not configured (NewClient called with empty cfgDir)")
	}

	parsed, err := bundle.Unmarshal(bundleBytes)
	if err != nil {
		return fmt.Errorf("parse bundle: %w", err)
	}
	signable, err := parsed.Signable()
	if err != nil {
		return fmt.Errorf("rebuild signable: %w", err)
	}
	if _, err := trustanchor.Default().Verify(parsed.Signature, signable); err != nil {
		return fmt.Errorf("verify bundle: %w", err)
	}
	now := time.Now()
	if err := parsed.CheckExpiry(now); err != nil {
		return err
	}
	if err := parsed.CheckCPDeviceKeypair(); err != nil {
		return err
	}

	// Persist verified bytes for Run() to pick up. Raw form (not the parsed
	// struct) keeps the round-trip stable: a future Run() can re-verify
	// against the same signable bytes that just verified here, so a trust
	// anchor rotation between Import and Run is a clean revoke.
	bundlePath := c.cfgDir + "/bundle.cbor"
	if err := os.MkdirAll(c.cfgDir, 0o700); err != nil {
		return fmt.Errorf("mkdir cfgDir: %w", err)
	}
	tmp := bundlePath + ".tmp"
	if err := os.WriteFile(tmp, bundleBytes, 0o600); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	if err := os.Rename(tmp, bundlePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename bundle: %w", err)
	}
	return nil
}

// BundleCapabilities returns a JSON-encoded snapshot of which v0.2
// operating modes the persisted bundle can drive. Swift parses this into
// the BundleCapabilities struct and uses it to gate the mode selector
// (single-capability bundle locks the UI to one mode; both-capabilities
// bundle surfaces the three-mode picker).
//
//	{ "wg_cp0": bool, "inner_mesh": bool, "has_mobile_cert": bool }
//
// Returns the all-false JSON when no bundle is imported yet. Does NOT
// re-verify the bundle signature: ImportBundle already verified at write
// time, and field-shape inspection here does not need crypto. If the
// bundle on disk is corrupt, returns all-false defensively.
func (c *Client) BundleCapabilities() string {
	if c.cfgDir == "" {
		return capsJSON(false, false, false)
	}
	raw, err := os.ReadFile(c.cfgDir + "/bundle.cbor")
	if err != nil {
		return capsJSON(false, false, false)
	}
	parsed, err := bundle.Unmarshal(raw)
	if err != nil {
		return capsJSON(false, false, false)
	}
	return capsJSON(parsed.HasWgCp0(), parsed.HasInnerMesh(), parsed.HasMobileCert())
}

// capsJSON renders the BundleCapabilities JSON shape. Hand-rolled
// rather than json.Marshal because the field set is tiny.
func capsJSON(wgCp0, innerMesh, hasMobileCert bool) string {
	return fmt.Sprintf(`{"wg_cp0":%t,"inner_mesh":%t,"has_mobile_cert":%t}`, wgCp0, innerMesh, hasMobileCert)
}

// Run starts the tunnel goroutine and blocks until Stop is called or the
// underlying tunnel exits. Called from the Swift NEPacketTunnelProvider's
// startTunnel(options:completionHandler:) — typically on a background
// dispatch queue.
//
// fd: the utun file descriptor the NEPacketTunnelProvider's packetFlow exposes
// (Swift extracts it via the well-known socket dance).
//
// interfaceName: the wg interface name (e.g. "utun5"); informational, the FD
// is what actually gets driven.
//
// envList: optional env-var bag pre-applied to os.Setenv before tunnel start.
// May be nil.
func (c *Client) Run(fd int32, interfaceName string, envList *EnvList) error {
	applyEnv(envList)
	c.stateAtomic.Store(StateConnecting)

	bundlePath := c.cfgDir + "/bundle.cbor"
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		c.stateAtomic.Store(StateError)
		if os.IsNotExist(err) {
			return ErrNoBundleImported
		}
		return fmt.Errorf("read bundle: %w", err)
	}
	parsed, err := bundle.Unmarshal(raw)
	if err != nil {
		c.stateAtomic.Store(StateError)
		return fmt.Errorf("parse bundle: %w", err)
	}
	signable, err := parsed.Signable()
	if err != nil {
		c.stateAtomic.Store(StateError)
		return fmt.Errorf("rebuild signable: %w", err)
	}
	if _, err := trustanchor.Default().Verify(parsed.Signature, signable); err != nil {
		c.stateAtomic.Store(StateError)
		return fmt.Errorf("verify bundle: %w", err)
	}
	if err := parsed.CheckExpiry(time.Now()); err != nil {
		c.stateAtomic.Store(StateError)
		return err
	}

	c.mu.Lock()
	mode := c.mode
	if mode == "" {
		mode = "wg-cp0-only" // v0.1.x default if native shell did not SetMode
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.ctxCancel = cancel
	c.mu.Unlock()
	defer cancel()

	hasOuter := mode == "wg-cp0-only" || mode == "combined"
	hasInner := mode == "netbird-only" || mode == "combined"

	// Bring up inner mesh first when the mode includes it. Connect()
	// returns on initial-up or ctx cancel; the subsystem keeps running
	// in its own goroutines after Connect returns.
	if hasInner {
		imCfg, err := innermesh.FromBundle(parsed)
		if err != nil {
			c.stateAtomic.Store(StateError)
			return fmt.Errorf("inner mesh config from bundle: %w", err)
		}
		mesh := innermesh.New()
		if err := mesh.Configure(imCfg); err != nil {
			c.stateAtomic.Store(StateError)
			_ = mesh.Close()
			return fmt.Errorf("inner mesh configure: %w", err)
		}
		if err := mesh.Connect(ctx); err != nil {
			c.stateAtomic.Store(StateError)
			_ = mesh.Close()
			return fmt.Errorf("inner mesh connect: %w", err)
		}
		c.mu.Lock()
		c.innerMesh = mesh
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			m := c.innerMesh
			c.innerMesh = nil
			c.mu.Unlock()
			if m != nil {
				discCtx, discCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = m.Disconnect(discCtx)
				discCancel()
				_ = m.Close()
			}
		}()
	}

	c.stateAtomic.Store(StateConnected)

	if hasOuter {
		cfg, err := tunnel.FromBundle(parsed)
		if err != nil {
			c.stateAtomic.Store(StateError)
			return fmt.Errorf("derive tunnel config: %w", err)
		}
		if interfaceName != "" {
			cfg.InterfaceName = interfaceName
		}
		runErr := tunnel.RunOnMobile(ctx, int(fd), cfg.InterfaceName, &cfg, nil)
		if runErr != nil {
			c.stateAtomic.Store(StateError)
			return runErr
		}
	} else {
		// netbird-only: no wg-cp0 outer tunnel. Block until Stop()
		// cancels ctx. The inner mesh keeps running in its own
		// goroutines; cancellation walks the deferred cleanup above.
		<-ctx.Done()
	}

	c.stateAtomic.Store(StateDisconnected)
	return nil
}

// Stop signals the running tunnel to wind down. Idempotent — safe to call
// multiple times or before Run.
func (c *Client) Stop() {
	c.mu.Lock()
	cancel := c.ctxCancel
	c.ctxCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.stateAtomic.Store(StateDisconnected)
}

// GetTunnelStatus returns one of StateDisconnected / StateConnecting /
// StateConnected / StateError. Bare-state getter kept for backward
// compatibility with the v0.1.x Swift callers; the v0.2 status surface
// is GetStatusJSON below.
func (c *Client) GetTunnelStatus() string {
	v, _ := c.stateAtomic.Load().(string)
	if v == "" {
		return StateDisconnected
	}
	return v
}

// SetMode tells the SDK which v0.2 operating mode the next Run will
// drive. Accepts the canonical kebab-case raw values from
// internal/mode (wg-cp0-only / netbird-only / combined); unknown
// strings are stored as-is so the daemon-side validation surface
// (when introduced) stays the single source of truth. Safe to call
// before Run, between Stop and Run, or during a mode switch.
func (c *Client) SetMode(mode string) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

// GetMode returns the mode the SDK will dispatch on for the next Run.
// Empty string when SetMode has not been called; the native shell is
// expected to call SetMode at app launch (reading from App Group
// UserDefaults) and on every mode switch.
func (c *Client) GetMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// GetStatusJSON returns the v0.2 status snapshot as a JSON object.
// Shape mirrors the desktop daemon's internal/ipc.StatusInfo for
// behaviour parity (docs/parity-audit-desktop-vs-mobile.md §1):
//
//	{
//	  "state":         "connected",
//	  "mode":          "combined",
//	  "interface_name": "utun-goat",
//	  "bundle_imported": true,
//	  "inner_mesh":    null      // populated by iteration-3 wiring
//	}
//
// Mode + inner_mesh are the v0.2 additions; existing fields keep
// their v0.1.x semantics. The Swift StatusSnapshot parser tolerates
// missing optional fields so v0.1.x bundles + builds without the
// inner-mesh subsystem render correctly.
func (c *Client) GetStatusJSON() string {
	state, _ := c.stateAtomic.Load().(string)
	if state == "" {
		state = StateDisconnected
	}
	c.mu.Lock()
	mode := c.mode
	mesh := c.innerMesh
	c.mu.Unlock()
	haveBundle := false
	if c.cfgDir != "" {
		if _, err := os.Stat(c.cfgDir + "/bundle.cbor"); err == nil {
			haveBundle = true
		}
	}
	innerJSON := "null"
	if mesh != nil {
		st := mesh.State().String()
		stats, _ := mesh.Stats()
		innerJSON = fmt.Sprintf(`{"state":%q,"peer_count":%d,"bytes_in":%d,"bytes_out":%d}`,
			st, stats.PeerCount, stats.BytesIn, stats.BytesOut)
	}
	return fmt.Sprintf(`{"state":%q,"mode":%q,"bundle_imported":%t,"inner_mesh":%s}`, state, mode, haveBundle, innerJSON)
}

// SetCustomLogger lets Swift attach an os_log-backed logger after NewClient.
// Optional. Currently a no-op until logger.go grows a real sink — recorded
// here so the gomobile binding API is stable.
func (c *Client) SetCustomLogger(_ CustomLogger) {
	// reserved
}

func applyEnv(list *EnvList) {
	for k, v := range list.allItems() {
		_ = os.Setenv(k, v)
	}
}
