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

	cfg, err := tunnel.FromBundle(parsed)
	if err != nil {
		c.stateAtomic.Store(StateError)
		return fmt.Errorf("derive tunnel config: %w", err)
	}
	if interfaceName != "" {
		cfg.InterfaceName = interfaceName
	}

	c.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	c.ctxCancel = cancel
	c.mu.Unlock()
	defer cancel()

	// Optimistic connected: tunnel.RunOnMobile only returns on Stop or a
	// fatal device error, so flipping to Connected before blocking gives
	// the Swift UI a usable signal. Real handshake-watching is a follow-up
	// (it would tail Stats() through a goroutine; out of scope here).
	c.stateAtomic.Store(StateConnected)
	runErr := tunnel.RunOnMobile(ctx, int(fd), cfg.InterfaceName, &cfg, nil)
	if runErr != nil {
		c.stateAtomic.Store(StateError)
		return runErr
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
// StateConnected / StateError. The Swift main-app polls this for the
// status pane; the NEPacketTunnelProvider also reads it after Run returns.
func (c *Client) GetTunnelStatus() string {
	v, _ := c.stateAtomic.Load().(string)
	if v == "" {
		return StateDisconnected
	}
	return v
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
