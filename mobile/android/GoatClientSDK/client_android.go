//go:build android

package goatclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/trustanchor"
	"github.com/dlf-dds/goat-client/internal/tunnel"
)

// Connection states surfaced by GetTunnelStatus(). Constants are exported
// so the Kotlin shell doesn't have to hard-code string literals.
const (
	StateUnconfigured = "unconfigured" // no bundle imported yet
	StateImported     = "imported"     // bundle imported, tunnel not started
	StateConnecting   = "connecting"   // Run() in progress, handshake pending
	StateConnected    = "connected"    // wg-cp0 handshake complete
	StateDisconnected = "disconnected" // tunnel stopped
	StateError        = "error"        // see Reason field
)

// Client is the gomobile-bound entry point. The Kotlin shell holds one
// instance per app process; lifecycle is bound to GoatVpnService.
//
// Reshaped from netbird client/android/Client: Login / IsLoginRequired
// / Networks / PeersList / route management stripped (single-peer
// wg-cp0 has none of those concepts). Added: ImportBundle (file picker
// or QR scan upload path) + GetTunnelStatus (UI poll path).
type Client struct {
	deviceName string
	uiVersion  string

	tunAdapter            TunAdapter
	iFaceDiscover         IFaceDiscover
	networkChangeListener NetworkChangeListener

	mu        sync.RWMutex
	files     PlatformFiles
	state     string
	reason    string // populated when state == StateError
	since     time.Time
	bundleSum string // hex-encoded SHA-256 of last imported bundle, for UI
	mode      string // v0.2 operating mode: wg-cp0-only / netbird-only / combined
	innerMesh innermesh.Mesh // populated when mode includes inner mesh; nil otherwise

	ctxCancel context.CancelFunc
}

// NewClient returns a new Client. Lifecycle:
//
//	c := NewClient(31, "Pixel 8", "0.0.1", vpnSvc, vpnSvc, vpnSvc)
//	c.ImportBundle(bytesFromPicker)              // one-time, or on bundle rotation
//	c.Run(platformFiles, dnsList, listener, envList) // blocks until Stop()
//
// The androidSDKVersion arg is a hint to the engine for OS-specific
// workarounds (e.g. pidfd seccomp policy on API ≤30); see netbird
// client/android/exec.go for the analogue.
func NewClient(androidSDKVersion int, deviceName string, uiVersion string, tunAdapter TunAdapter, iFaceDiscover IFaceDiscover, networkChangeListener NetworkChangeListener) *Client {
	setAndroidProtectSocketFn(tunAdapter.ProtectSocket)
	return &Client{
		deviceName:            deviceName,
		uiVersion:             uiVersion,
		tunAdapter:            tunAdapter,
		iFaceDiscover:         iFaceDiscover,
		networkChangeListener: networkChangeListener,
		state:                 StateUnconfigured,
		since:                 time.Now(),
	}
}

// ImportBundle accepts the raw bytes of an offline-CA-signed CBOR bundle
// (per goat-trunk docs/design/offline-enrollment.md), parses it, verifies
// the Ed25519 signature against the build-time-pinned trust anchors
// (internal/trustanchor.Default), and persists the raw bytes at the
// configured ConfigurationFilePath for Run() to re-load.
//
// Returns an error on:
//   - empty input (caller should verify file/QR payload before calling)
//   - bundle parse / signature verify failure
//   - filesystem write failure (Android sandbox / disk full)
func (c *Client) ImportBundle(bundleBytes []byte) error {
	if len(bundleBytes) == 0 {
		return errors.New("import bundle: empty payload")
	}
	c.mu.Lock()
	files := c.files
	c.mu.Unlock()

	if files == nil {
		return errors.New("import bundle: PlatformFiles not yet attached; call Configure() first")
	}

	cfgPath := files.ConfigurationFilePath()
	if cfgPath == "" {
		return errors.New("import bundle: PlatformFiles.ConfigurationFilePath() returned empty")
	}

	parsed, err := bundle.Unmarshal(bundleBytes)
	if err != nil {
		return fmt.Errorf("import bundle: parse: %w", err)
	}
	signable, err := parsed.Signable()
	if err != nil {
		return fmt.Errorf("import bundle: rebuild signable: %w", err)
	}
	if _, err := trustanchor.Default().Verify(parsed.Signature, signable); err != nil {
		return fmt.Errorf("import bundle: verify: %w", err)
	}
	now := time.Now()
	if err := parsed.CheckExpiry(now); err != nil {
		return err
	}
	if err := parsed.CheckCPDeviceKeypair(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return fmt.Errorf("import bundle: mkdir parent: %w", err)
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, bundleBytes, 0o600); err != nil {
		return fmt.Errorf("import bundle: write temp: %w", err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("import bundle: rename: %w", err)
	}

	sum := bundleChecksum(bundleBytes)
	c.mu.Lock()
	c.bundleSum = sum
	if c.state == StateUnconfigured || c.state == StateError {
		c.state = StateImported
		c.reason = ""
		c.since = time.Now()
	}
	c.mu.Unlock()
	return nil
}

// BundleCapabilities returns a JSON-encoded snapshot of which v0.2
// operating modes the persisted bundle can drive. Kotlin parses this
// and uses it to gate the mode selector (single-capability bundle locks
// the UI to one mode; both-capabilities surfaces the three-mode picker).
//
//	{ "wg_cp0": bool, "inner_mesh": bool, "has_mobile_cert": bool }
//
// Returns the all-false JSON when no bundle is imported yet. Does NOT
// re-verify the bundle signature: ImportBundle already verified at write
// time, and field-shape inspection here does not need crypto.
func (c *Client) BundleCapabilities() string {
	c.mu.RLock()
	files := c.files
	c.mu.RUnlock()
	if files == nil {
		return capsJSON(false, false, false)
	}
	cfgPath := files.ConfigurationFilePath()
	if cfgPath == "" {
		return capsJSON(false, false, false)
	}
	raw, err := os.ReadFile(cfgPath)
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

// Configure attaches PlatformFiles without starting the engine. Useful
// when the Kotlin shell wants to support an Import-without-Connect UX
// (user adds a bundle, leaves the app, returns later to start the
// tunnel). Run() also accepts PlatformFiles and will overwrite.
func (c *Client) Configure(files PlatformFiles) {
	c.mu.Lock()
	c.files = files
	c.mu.Unlock()
}

// Run starts the wg-cp0 outer tunnel and blocks until Stop is called or
// a fatal error occurs. Returns nil on clean Stop, error otherwise.
//
// Shape preserved from netbird client/android/Client.Run minus the
// urlOpener+isAndroidTV args (no SSO flow on goat). Args:
//   - files: per-app filesystem paths
//   - dns:   bundle-supplied DNS resolvers (may be empty)
//   - dnsReadyListener: fires when DNS is in effect
//   - envList: tunable env vars (force-relay et al)
//
// The engine wires the underlying ProtectSocket bridge during NewClient
// (setAndroidProtectSocketFn). Plumbing through wireguard-go's outer UDP
// socket is the next iteration; see protect_android.go for the SDK-side
// half.
func (c *Client) Run(files PlatformFiles, dns *DNSList, dnsReadyListener DnsReadyListener, envList *EnvList) error {
	c.mu.Lock()
	c.files = files
	c.state = StateConnecting
	c.reason = ""
	c.since = time.Now()
	mode := c.mode
	if mode == "" {
		mode = "wg-cp0-only" // v0.1.x default if Kotlin shell did not SetMode
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.ctxCancel = cancel
	c.mu.Unlock()
	defer cancel()

	exportEnv(envList)

	cfgPath, parsed, err := c.loadVerifiedBundle()
	if err != nil {
		c.fail(err.Error())
		return err
	}
	_ = cfgPath // retained for future audit logging

	hasOuter := mode == "wg-cp0-only" || mode == "combined"
	hasInner := mode == "netbird-only" || mode == "combined"

	// Bring up inner mesh first when the mode includes it.
	if hasInner {
		imCfg, err := innermesh.FromBundle(parsed)
		if err != nil {
			c.fail(err.Error())
			return fmt.Errorf("inner mesh config from bundle: %w", err)
		}
		mesh := innermesh.New()
		if err := mesh.Configure(imCfg); err != nil {
			c.fail(err.Error())
			_ = mesh.Close()
			return fmt.Errorf("inner mesh configure: %w", err)
		}
		if err := mesh.Connect(ctx); err != nil {
			c.fail(err.Error())
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

	if !hasOuter {
		// netbird-only: no wg-cp0 outer tunnel. Block until Stop()
		// cancels ctx. The inner mesh keeps running in its own
		// goroutines; cancellation walks the deferred cleanup above.
		c.mu.Lock()
		c.state = StateConnected
		c.since = time.Now()
		c.mu.Unlock()
		<-ctx.Done()
		c.mu.Lock()
		c.state = StateDisconnected
		c.since = time.Now()
		c.mu.Unlock()
		return nil
	}

	cfg, err := tunnel.FromBundle(parsed)
	if err != nil {
		c.fail(err.Error())
		return fmt.Errorf("derive tunnel config: %w", err)
	}

	dnsAddrs := dnsListSnapshot(dns)
	dnsCSV := joinDNSAddrs(dnsAddrs)
	routesCSV := joinAllowedIPs(cfg.Peer.AllowedIPs)

	fd, err := c.tunAdapter.ConfigureInterface(
		cfg.LocalAddress.String(),
		int(cfg.MTU),
		dnsCSV,
		"",
		routesCSV,
	)
	if err != nil {
		c.fail(err.Error())
		return fmt.Errorf("VpnService configure: %w", err)
	}
	if fd < 0 {
		err := fmt.Errorf("VpnService returned invalid fd %d", fd)
		c.fail(err.Error())
		return err
	}

	if dnsReadyListener != nil {
		dnsReadyListener.OnReady()
	}

	c.mu.Lock()
	c.state = StateConnected
	c.since = time.Now()
	c.mu.Unlock()

	runErr := tunnel.RunOnMobile(ctx, fd, cfg.InterfaceName, &cfg, dnsAddrs)

	c.mu.Lock()
	if runErr != nil {
		c.state = StateError
		c.reason = runErr.Error()
	} else {
		c.state = StateDisconnected
	}
	c.since = time.Now()
	c.mu.Unlock()
	return runErr
}

// Stop signals the engine to tear down the tunnel. Safe to call
// multiple times; safe to call before Run.
func (c *Client) Stop() {
	c.mu.Lock()
	cancel := c.ctxCancel
	c.ctxCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RenewTun is called by Kotlin when the system gives back a fresh
// utun fd (e.g. after VpnService.Builder rebuild on a network change).
// Not yet implemented — the engine currently does not support hot-swap
// of the underlying tun fd. Stop()+Run() is the supported path.
func (c *Client) RenewTun(fd int) error {
	if fd < 0 {
		return fmt.Errorf("renew tun: invalid fd %d", fd)
	}
	return errors.New("renew tun: in-place fd swap not yet supported; call Stop() then Run()")
}

// SetTraceLogLevel / SetInfoLogLevel are stubs that match netbird's
// gomobile API surface so the Kotlin shell can compile against either
// engine during the converge window.
func (c *Client) SetTraceLogLevel() {}
func (c *Client) SetInfoLogLevel()  {}

// GetTunnelStatus returns a JSON-encoded status snapshot for the UI:
//
//	{
//	  "state":      "connected",
//	  "reason":     "",
//	  "since":      "2026-05-09T22:30:00Z",
//	  "bundleSum":  "<sha256-hex>",
//	  "deviceName": "Pixel 8"
//	}
//
// Kotlin polls this on the status pane (cheap; in-memory). For
// per-second handshake / bytes-in/out, a future hook will add a separate
// streaming RPC; this method stays for the at-a-glance UI poll.
func (c *Client) GetTunnelStatus() string {
	c.mu.RLock()
	type innerSnap struct {
		State     string `json:"state"`
		PeerCount int    `json:"peer_count"`
		BytesIn   uint64 `json:"bytes_in"`
		BytesOut  uint64 `json:"bytes_out"`
	}
	var imField interface{}
	if c.innerMesh != nil {
		st := c.innerMesh.State().String()
		stats, _ := c.innerMesh.Stats()
		imField = innerSnap{State: st, PeerCount: stats.PeerCount, BytesIn: stats.BytesIn, BytesOut: stats.BytesOut}
	}
	snap := struct {
		State      string      `json:"state"`
		Reason     string      `json:"reason,omitempty"`
		Since      string      `json:"since"`
		BundleSum  string      `json:"bundleSum,omitempty"`
		DeviceName string      `json:"deviceName,omitempty"`
		Mode       string      `json:"mode,omitempty"`
		InnerMesh  interface{} `json:"inner_mesh"`
	}{
		State:      c.state,
		Reason:     c.reason,
		Since:      c.since.UTC().Format(time.RFC3339),
		BundleSum:  c.bundleSum,
		DeviceName: c.deviceName,
		Mode:       c.mode,
		InnerMesh:  imField,
	}
	c.mu.RUnlock()
	b, err := json.Marshal(snap)
	if err != nil {
		return `{"state":"error","reason":"status marshal failed"}`
	}
	return string(b)
}

// SetMode tells the SDK which v0.2 operating mode the next Run will
// drive. Accepts the canonical kebab-case raw values from
// internal/mode (wg-cp0-only / netbird-only / combined). Safe to call
// before Run, between Stop and Run, or during a mode switch.
func (c *Client) SetMode(mode string) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

// GetMode returns the mode the SDK will dispatch on for the next Run.
// Empty string when SetMode has not been called.
func (c *Client) GetMode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

// loadVerifiedBundle reads the persisted bundle and re-verifies it against
// the pinned trust anchors. Re-verification on every Run lets a trust
// anchor rotation between Import and Run cleanly revoke an in-flight
// session that was authorised under an older root.
func (c *Client) loadVerifiedBundle() (string, *bundle.EnrollmentBundle, error) {
	c.mu.RLock()
	files := c.files
	c.mu.RUnlock()
	if files == nil {
		return "", nil, errors.New("no PlatformFiles attached")
	}
	cfgPath := files.ConfigurationFilePath()
	if cfgPath == "" {
		return "", nil, errors.New("ConfigurationFilePath empty")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, errors.New("no bundle imported; call ImportBundle first")
		}
		return "", nil, fmt.Errorf("read bundle: %w", err)
	}
	parsed, err := bundle.Unmarshal(raw)
	if err != nil {
		return "", nil, fmt.Errorf("parse bundle: %w", err)
	}
	signable, err := parsed.Signable()
	if err != nil {
		return "", nil, fmt.Errorf("rebuild signable: %w", err)
	}
	if _, err := trustanchor.Default().Verify(parsed.Signature, signable); err != nil {
		return "", nil, fmt.Errorf("verify bundle: %w", err)
	}
	if err := parsed.CheckExpiry(time.Now()); err != nil {
		return "", nil, err
	}
	return cfgPath, parsed, nil
}

func (c *Client) fail(reason string) {
	c.mu.Lock()
	c.state = StateError
	c.reason = reason
	c.since = time.Now()
	c.mu.Unlock()
}

func exportEnv(envList *EnvList) {
	if envList == nil {
		return
	}
	for k, v := range envList.snapshot() {
		_ = os.Setenv(k, v)
	}
}

func dnsListSnapshot(d *DNSList) []netip.Addr {
	if d == nil {
		return nil
	}
	aps := d.snapshot()
	out := make([]netip.Addr, 0, len(aps))
	for _, ap := range aps {
		out = append(out, ap.Addr())
	}
	return out
}

// joinDNSAddrs renders the resolver list as a comma-separated string, the
// shape TunAdapter.ConfigureInterface expects (Kotlin splits on `,` then
// addDnsServer per entry).
func joinDNSAddrs(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ",")
}

// joinAllowedIPs renders the AllowedIPs CIDR list as a comma-separated
// string for VpnService.Builder.addRoute (Kotlin splits and parses each).
func joinAllowedIPs(prefixes []netip.Prefix) string {
	if len(prefixes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ",")
}
