package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlf-dds/goat-client/internal/mode"
)

// The embedded netbird client must persist its config (the WireGuard
// private key) and cleanup state per profile. An empty ConfigPath
// makes the embed hold config in memory and mint a NEW WireGuard
// identity on every Connect — a new mgmt-registered peer + a consumed
// setup-key use per daemon restart. These tests pin the per-profile
// path derivation.

func TestMeshConfigCarriesPersistPaths(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	cfg := d.meshConfig(nil)
	if cfg.ConfigPath == "" || cfg.StatePath == "" {
		t.Fatalf("meshConfig persist paths empty: config=%q state=%q", cfg.ConfigPath, cfg.StatePath)
	}
	wantDir := filepath.Join(d.store.Dir(), "default.innermesh")
	if filepath.Dir(cfg.ConfigPath) != wantDir || filepath.Dir(cfg.StatePath) != wantDir {
		t.Errorf("persist paths not under %q: config=%q state=%q", wantDir, cfg.ConfigPath, cfg.StatePath)
	}
	if cfg.ConfigPath == cfg.StatePath {
		t.Errorf("config and state must be distinct files: both %q", cfg.ConfigPath)
	}
}

func TestMeshConfigPersistPathsFollowProfileSlug(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	d.mu.Lock()
	d.currentSlug = "efdi-lab"
	d.mu.Unlock()
	cfg := d.meshConfig(nil)
	if !strings.Contains(cfg.ConfigPath, "efdi-lab.innermesh") {
		t.Errorf("ConfigPath %q does not embed profile slug dir", cfg.ConfigPath)
	}
	other := d.meshConfig(nil)
	d.mu.Lock()
	d.currentSlug = "other-net"
	d.mu.Unlock()
	changed := d.meshConfig(nil)
	if other.ConfigPath == changed.ConfigPath {
		t.Errorf("distinct profiles must not share a persisted netbird config: %q", other.ConfigPath)
	}
}
