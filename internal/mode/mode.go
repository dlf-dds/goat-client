// Package mode defines the v0.2 three-mode tunnel-subsystem selector and
// its on-disk persistence shape.
//
// The three modes track ADR 0840 Amendment 2026-05-13 / Block 76 v0.2:
//
//   - WGCP0Only — only the outer wg-cp0 tunnel runs (the v0.1.x shape).
//     Used on desktop boxes that want the outer tunnel only.
//   - NetbirdOnly — only the inner mesh runs. Used on desktops where the
//     outer tunnel is supplied by the OS (e.g. an Orin site with kernel
//     WG already up) and goat-client is only responsible for the inner
//     mesh layer.
//   - Combined — both layers run. The outer wg-cp0 carries the inner mesh
//     traffic. This is the mobile-equivalent shape (one PacketTunnelProvider
//     hosting both layers) brought to desktop.
//
// The mode is selected at install time (packaging --mode argument or
// post-install prompt) and persisted; runtime override is via the
// `goat-client setmode <mode>` CLI which sends a setMode IPC to the
// daemon. Switching mode tears down the active subsystem(s) and brings
// the new mode's set up — in <30s per the verdict gate.
package mode

import (
	"errors"
	"strings"
)

// Mode is the active-subsystems selector.
type Mode string

const (
	// WGCP0Only keeps only the wg-cp0 outer tunnel up. The v0.1.x default.
	WGCP0Only Mode = "wg-cp0-only"

	// NetbirdOnly keeps only the inner-mesh netbird-derived subsystem up.
	NetbirdOnly Mode = "netbird-only"

	// Combined keeps both up: wg-cp0 outer + inner-mesh netbird.
	Combined Mode = "combined"
)

// Default is the v0.2 install default: combined matches the steady-state
// design intent of one app driving both layers. Packagers can override
// per-install via --mode.
const Default = Combined

// All returns the modes a user-facing UI may surface, in display order.
func All() []Mode { return []Mode{WGCP0Only, NetbirdOnly, Combined} }

// ErrUnknownMode is returned when a string does not parse to a known Mode.
var ErrUnknownMode = errors.New("mode: unknown mode")

// Parse normalises arbitrary user input (Settings panel, CLI flag,
// installer argument) into a Mode. Case-insensitive; accepts the
// canonical kebab-case form plus a few obvious aliases.
func Parse(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(WGCP0Only), "wg-cp0", "wgcp0", "outer", "outer-only":
		return WGCP0Only, nil
	case string(NetbirdOnly), "netbird", "inner", "inner-only":
		return NetbirdOnly, nil
	case string(Combined), "both", "all":
		return Combined, nil
	}
	return "", ErrUnknownMode
}

// String renders the canonical kebab-case form (used on the wire and on
// disk).
func (m Mode) String() string { return string(m) }

// Valid reports whether m is one of the three known modes.
func (m Mode) Valid() bool {
	switch m {
	case WGCP0Only, NetbirdOnly, Combined:
		return true
	}
	return false
}

// Display returns a short human-readable label for UI surfaces (Settings
// panel radio labels, tray menu tooltips). Distinct from String which
// is the on-wire identifier.
func (m Mode) Display() string {
	switch m {
	case WGCP0Only:
		return "wg-cp0 only (outer tunnel)"
	case NetbirdOnly:
		return "netbird only (inner mesh)"
	case Combined:
		return "combined (outer + inner)"
	}
	return string(m)
}

// Includes reports whether m runs the outer wg-cp0 tunnel.
func (m Mode) IncludesWGCP0() bool { return m == WGCP0Only || m == Combined }

// IncludesNetbird reports whether m runs the inner-mesh netbird subsystem.
func (m Mode) IncludesNetbird() bool { return m == NetbirdOnly || m == Combined }
