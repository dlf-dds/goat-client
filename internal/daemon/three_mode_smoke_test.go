// three_mode_smoke_test.go — Block 76N M4: hermetic three-mode smoke.
//
// Spawns the daemon orchestrator for each of {wg-cp0-only, netbird-only,
// combined} as goroutines inside the test binary, verifies the declared
// tunnels come up, and asserts GetStatus reports the per-mode shape.
//
// For wg-cp0-only the test uses a fakeTunnel (no real TUN device, no
// CAP_NET_ADMIN). For netbird-only and combined, the inner mesh is the
// real innermesh.Netbird wired to in-process fakemgmt+fakesignal — the
// same harness as TestNetbird_LifecycleAgainstFakes (PR #43). The
// combined-mode test runs both legs simultaneously inside one Daemon
// to exercise the path-A architecture (one process, both tunnels).
//
// The real-netbird modes also exercise M3 (Stats/Logs during a real
// session) by sampling mesh.Stats() + mesh.Logs() after Connect.

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/innermesh/fakemgmt"
	"github.com/dlf-dds/goat-client/internal/innermesh/fakesignal"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
	"github.com/dlf-dds/goat-client/internal/tunnel"
)

// smokeBudget is the per-mode time budget for the netbird-touching
// smokes. 45s leaves headroom over the ~6-7s lifecycle test observed
// in PR #43 while keeping the suite under a minute total.
const smokeBudget = 45 * time.Second

// skipNetbirdEmbedRaceProneTarget reports whether the current build
// environment is one where the vendored netbird embed.Client
// connect/stop race surfaces reliably. The race itself is platform-
// independent (a known data race between embed.Client.Start's
// engine-state writes and embed.Client.Stop's read on shutdown) but
// it only fires on environments where the scheduler interleaves
// "connect goroutine still running" with "Stop called" — namely:
//
//   - any build run with -race (the race detector forces enough
//     interleaving to hit it deterministically), AND
//   - windows/arm64 PR-gate jobs, where the runner doesn't ship a
//     race detector but its slower-than-native execution hits the
//     same window timing-wise. Observed first on PR #56's CI run
//     2026-05-22; pre-existing in main since PR #50's New() flip.
//
// Skipping is correct: a proper fix needs a sync patch to
// dlf-dds/netbird@client/embed/embed.go, tracked separately. The
// other PR-gate matrix legs (linux/{amd64,arm64}, darwin/{amd64,arm64},
// windows/amd64) all pass deterministically, so functional
// regressions in the real-netbird modes still gate merges on every
// other runner.
func skipNetbirdEmbedRaceProneTarget(t *testing.T) bool {
	t.Helper()
	if raceDetectorEnabled {
		t.Skip("upstream netbird embed.Client connect/stop race; tracked separately")
		return true
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		t.Skip("upstream netbird embed.Client connect/stop race surfaces on windows/arm64 timing; tracked separately")
		return true
	}
	return false
}

// TestThreeModeSmoke_WGCP0Only: outer-tunnel-only, fake tunnel only.
// Mesh is never constructed; this is the v0.1.x regression bar.
func TestThreeModeSmoke_WGCP0Only(t *testing.T) {
	t.Parallel()

	tb := mintTestBundle(t, true /* wg-cp0 */, "" /* no inner-mesh */)
	dir := writeBundleFile(t, tb.bytes)
	ft := newFakeTunnel()

	d, err := New(Config{
		BundlePath:    filepath.Join(dir, "bundle.cbor"),
		SocketPath:    filepath.Join(dir, "ipc.sock"),
		ConfigPath:    filepath.Join(dir, "config.toml"),
		TrustRoots:    tb.roots,
		InitialMode:   mode.WGCP0Only,
		TunnelFactory: func() TunnelManager { return ft },
		InnerMeshFactory: func() innermesh.Mesh {
			t.Fatal("InnerMeshFactory called in wg-cp0-only mode")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.LoadPersistedBundle(); err != nil {
		t.Fatalf("LoadPersistedBundle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if ft.connectCalls != 1 {
		t.Errorf("fakeTunnel.connectCalls = %d, want 1", ft.connectCalls)
	}
	if got := ft.State(); got != tunnel.StateUp {
		t.Errorf("fakeTunnel.State = %v, want up", got)
	}

	st, err := d.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Mode != string(mode.WGCP0Only) {
		t.Errorf("Mode = %q, want %q", st.Mode, mode.WGCP0Only)
	}
	if st.State != ipc.WireStateConnected {
		t.Errorf("outer State = %q, want connected", st.State)
	}
	if st.InnerMesh != nil {
		t.Errorf("InnerMesh = %+v, want nil (mode does not include netbird)", st.InnerMesh)
	}

	if err := d.Disconnect(ctx); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if ft.disconnectCalls != 1 {
		t.Errorf("fakeTunnel.disconnectCalls = %d, want 1", ft.disconnectCalls)
	}
}

// TestThreeModeSmoke_NetbirdOnly: inner-mesh-only, real Netbird against
// fakemgmt+fakesignal. Wg-cp0 leg untouched.
//
// Intentionally no t.Parallel() here (and no t.Parallel() on
// TestThreeModeSmoke_Combined either): embed.New mutates
// NB_USE_NETSTACK_MODE process-globally and the netbird logrus
// singleton isn't safe to share-init across goroutines. Adding
// t.Parallel() to either of the netbird-touching smokes risks
// data-race surfacing or worse, intermittent test corruption.
// Don't "fix the missing t.Parallel()" without first proving netbird
// embed handles concurrent New() — which it doesn't, as of the pin
// at goat-embed-ca-2026-05.
func TestThreeModeSmoke_NetbirdOnly(t *testing.T) {
	if skipNetbirdEmbedRaceProneTarget(t) {
		return
	}
	sig, mgmtURL := startInnerMeshFakes(t)
	_ = sig

	tb := mintTestBundle(t, false /* no wg-cp0 */, mgmtURL)
	dir := writeBundleFile(t, tb.bytes)
	ft := newFakeTunnel()
	mesh := innermesh.NewNetbird("smoke-netbirdonly")
	t.Cleanup(func() { _ = mesh.Close() })

	d, err := New(Config{
		BundlePath:       filepath.Join(dir, "bundle.cbor"),
		SocketPath:       filepath.Join(dir, "ipc.sock"),
		ConfigPath:       filepath.Join(dir, "config.toml"),
		TrustRoots:       tb.roots,
		InitialMode:      mode.NetbirdOnly,
		TunnelFactory:    func() TunnelManager { return ft },
		InnerMeshFactory: func() innermesh.Mesh { return mesh },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.LoadPersistedBundle(); err != nil {
		t.Fatalf("LoadPersistedBundle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), smokeBudget)
	defer cancel()
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v\nlogs:\n%s", err, strings.Join(mesh.Logs(50), "\n"))
	}

	if ft.connectCalls != 0 {
		t.Errorf("fakeTunnel.connectCalls = %d, want 0 (mode excludes wg-cp0)", ft.connectCalls)
	}
	if mesh.State() != innermesh.StateUp {
		t.Errorf("mesh.State = %v, want up\nlogs:\n%s",
			mesh.State(), strings.Join(mesh.Logs(50), "\n"))
	}

	st, err := d.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Mode != string(mode.NetbirdOnly) {
		t.Errorf("Mode = %q, want %q", st.Mode, mode.NetbirdOnly)
	}
	if st.InnerMesh == nil {
		t.Fatal("InnerMesh = nil; want populated")
	}
	if st.InnerMesh.State != ipc.WireStateConnected {
		t.Errorf("InnerMesh.State = %q, want connected", st.InnerMesh.State)
	}

	// M3 check: Stats + Logs populate during a real session.
	stats, err := mesh.Stats()
	if err != nil {
		t.Errorf("mesh.Stats: %v", err)
	}
	// PeerCount == 0 against an empty fakemgmt is expected (no other peers
	// registered). The smoke is that Stats() round-trips without error,
	// proving client.Status() is reachable through the embed surface.
	t.Logf("mesh.Stats: peers=%d in=%d out=%d", stats.PeerCount, stats.BytesIn, stats.BytesOut)
	if logs := mesh.Logs(0); len(logs) == 0 {
		t.Error("mesh.Logs() = empty; M3 ring buffer not receiving netbird logrus output")
	} else {
		t.Logf("mesh.Logs: %d lines captured (head=%q)", len(logs), logs[0])
	}

	if err := d.Disconnect(ctx); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
}

// TestThreeModeSmoke_Combined: both legs active simultaneously inside
// one Daemon. Wg-cp0 fake tunnel + real Netbird against fakemgmt.
func TestThreeModeSmoke_Combined(t *testing.T) {
	if skipNetbirdEmbedRaceProneTarget(t) {
		return
	}
	sig, mgmtURL := startInnerMeshFakes(t)
	_ = sig

	tb := mintTestBundle(t, true /* wg-cp0 */, mgmtURL)
	dir := writeBundleFile(t, tb.bytes)
	ft := newFakeTunnel()
	mesh := innermesh.NewNetbird("smoke-combined")
	t.Cleanup(func() { _ = mesh.Close() })

	d, err := New(Config{
		BundlePath:       filepath.Join(dir, "bundle.cbor"),
		SocketPath:       filepath.Join(dir, "ipc.sock"),
		ConfigPath:       filepath.Join(dir, "config.toml"),
		TrustRoots:       tb.roots,
		InitialMode:      mode.Combined,
		TunnelFactory:    func() TunnelManager { return ft },
		InnerMeshFactory: func() innermesh.Mesh { return mesh },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.LoadPersistedBundle(); err != nil {
		t.Fatalf("LoadPersistedBundle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), smokeBudget)
	defer cancel()
	if err := d.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v\nlogs:\n%s", err, strings.Join(mesh.Logs(50), "\n"))
	}

	if ft.connectCalls != 1 {
		t.Errorf("fakeTunnel.connectCalls = %d, want 1", ft.connectCalls)
	}
	if got := ft.State(); got != tunnel.StateUp {
		t.Errorf("fakeTunnel.State = %v, want up", got)
	}
	if mesh.State() != innermesh.StateUp {
		t.Errorf("mesh.State = %v, want up", mesh.State())
	}

	st, err := d.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Mode != string(mode.Combined) {
		t.Errorf("Mode = %q, want %q", st.Mode, mode.Combined)
	}
	if st.State != ipc.WireStateConnected {
		t.Errorf("outer State = %q, want connected", st.State)
	}
	if st.InnerMesh == nil {
		t.Fatal("InnerMesh = nil; want populated")
	}
	if st.InnerMesh.State != ipc.WireStateConnected {
		t.Errorf("InnerMesh.State = %q, want connected", st.InnerMesh.State)
	}

	// M3 spot-check on the combined path too.
	if logs := mesh.Logs(0); len(logs) == 0 {
		t.Error("combined mesh.Logs() = empty; M3 wiring regressed")
	}

	if err := d.Disconnect(ctx); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if ft.disconnectCalls != 1 {
		t.Errorf("fakeTunnel.disconnectCalls = %d, want 1 after Disconnect", ft.disconnectCalls)
	}
}

// startInnerMeshFakes brings up the fakemgmt + fakesignal pair (same
// shape as TestNetbird_LifecycleAgainstFakes) and returns the signal
// server (so the test can hold a reference for t.Cleanup) plus the
// "http://" + mgmtAddr string used as the bundle's ManagementURL.
func startInnerMeshFakes(t *testing.T) (*fakesignal.Server, string) {
	t.Helper()
	sig, err := fakesignal.Listen(t)
	if err != nil {
		t.Fatalf("fakesignal.Listen: %v", err)
	}
	mgmt, err := fakemgmt.Listen(t, fakemgmt.WithSignalURI(sig.Addr()))
	if err != nil {
		t.Fatalf("fakemgmt.Listen: %v", err)
	}
	return sig, "http://" + mgmt.Addr()
}

// writeBundleFile drops `data` at <tempdir>/bundle.cbor and returns
// the tempdir. The daemon reads bundle.cbor via LoadPersistedBundle.
func writeBundleFile(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bundle.cbor"), data, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return dir
}

