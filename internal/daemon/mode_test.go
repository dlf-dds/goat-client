package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// newTestDaemon constructs a Daemon with paths inside t.TempDir and a
// Fake innermesh factory. TrustRoots intentionally nil — the tests
// drive mode transitions and never call ImportBundle.
func newTestDaemon(t *testing.T, initial mode.Mode) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{
		BundlePath:  filepath.Join(dir, "bundle.cbor"),
		SocketPath:  filepath.Join(dir, "ipc.sock"),
		ConfigPath:  filepath.Join(dir, "config.toml"),
		InitialMode: initial,
		InnerMeshFactory: func() innermesh.Mesh {
			return innermesh.NewFake()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestInitialModeFromConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := mode.Save(cfgPath, mode.PersistedConfig{Mode: mode.NetbirdOnly}); err != nil {
		t.Fatalf("save: %v", err)
	}
	d, err := New(Config{
		BundlePath:       filepath.Join(dir, "bundle.cbor"),
		SocketPath:       filepath.Join(dir, "ipc.sock"),
		ConfigPath:       cfgPath,
		InnerMeshFactory: func() innermesh.Mesh { return innermesh.NewFake() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reply, err := d.GetMode(context.Background())
	if err != nil {
		t.Fatalf("GetMode: %v", err)
	}
	if reply.Mode != string(mode.NetbirdOnly) {
		t.Errorf("GetMode=%q want %q", reply.Mode, mode.NetbirdOnly)
	}
}

func TestInitialModeOverridesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	_ = mode.Save(cfgPath, mode.PersistedConfig{Mode: mode.NetbirdOnly})
	d, err := New(Config{
		BundlePath:       filepath.Join(dir, "bundle.cbor"),
		SocketPath:       filepath.Join(dir, "ipc.sock"),
		ConfigPath:       cfgPath,
		InitialMode:      mode.WGCP0Only,
		InnerMeshFactory: func() innermesh.Mesh { return innermesh.NewFake() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	reply, _ := d.GetMode(context.Background())
	if reply.Mode != string(mode.WGCP0Only) {
		t.Errorf("explicit InitialMode should win; got %q", reply.Mode)
	}
}

func TestSetModeTransition(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.WGCP0Only)
	ctx := context.Background()
	prev, err := d.SetMode(ctx, ipc.SetModeRequest{Mode: string(mode.Combined)})
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if prev.PreviousMode != string(mode.WGCP0Only) {
		t.Errorf("previous=%q want %q", prev.PreviousMode, mode.WGCP0Only)
	}
	if prev.Mode != string(mode.Combined) {
		t.Errorf("new=%q want %q", prev.Mode, mode.Combined)
	}
	cur, _ := d.GetMode(ctx)
	if cur.Mode != string(mode.Combined) {
		t.Errorf("GetMode after: %q want %q", cur.Mode, mode.Combined)
	}
	// Persisted?
	loaded, err := mode.LoadOrDefault(d.cfg.ConfigPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != mode.Combined {
		t.Errorf("persisted=%q want %q", loaded, mode.Combined)
	}
}

func TestSetModeIdempotent(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.Combined)
	ctx := context.Background()
	prev, err := d.SetMode(ctx, ipc.SetModeRequest{Mode: string(mode.Combined)})
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if prev.PreviousMode != prev.Mode {
		t.Errorf("idempotent setMode should report previous==new; got %q→%q", prev.PreviousMode, prev.Mode)
	}
}

func TestSetModeRejectsUnknown(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.WGCP0Only)
	if _, err := d.SetMode(context.Background(), ipc.SetModeRequest{Mode: "bogus"}); err == nil {
		t.Error("expected error for unknown mode")
	}
}

func TestGetStatusReflectsMode(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.Combined)
	st, err := d.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Mode != string(mode.Combined) {
		t.Errorf("status mode=%q want %q", st.Mode, mode.Combined)
	}
	// InnerMesh is nil until Connect runs the mesh; just confirm Mode
	// flows through.
}

func TestGetStatusInNetbirdOnlyHasInnerMesh(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	// Bring the mesh up so InnerMeshSnapshot populates.
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}
	st, err := d.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.InnerMesh == nil {
		t.Fatal("InnerMesh expected in netbird-only mode")
	}
	if st.InnerMesh.State != ipc.WireStateConnected {
		t.Errorf("inner-mesh state=%q want connected", st.InnerMesh.State)
	}
}

// TestSetModeConfiguresInnerMeshFromBundle is the post-flip regression
// guard: SetMode-into-a-netbird-including-mode must Configure the new
// mesh from the loaded bundle before Connect. Pre-fix the bring-up
// branch called mesh.Connect directly — with the Fake this passed
// (Configure is optional); with the real Netbird it errored with
// "not configured" because Netbird.Configure validates ManagementURL.
// See docs/audits/2026-05-21-post-flip/parity-audit.md row "Mode
// switch — bring-up Configure call."
func TestSetModeConfiguresInnerMeshFromBundle(t *testing.T) {
	t.Parallel()

	tb := mintTestBundle(t, true /* wg-cp0 */, "http://mgmt.invalid:443")
	dir := writeBundleFile(t, tb.bytes)

	var (
		recorded   innermesh.Config
		configured bool
	)
	d, err := New(Config{
		BundlePath:    filepath.Join(dir, "bundle.cbor"),
		SocketPath:    filepath.Join(dir, "ipc.sock"),
		ConfigPath:    filepath.Join(dir, "config.toml"),
		TrustRoots:    tb.roots,
		InitialMode:   mode.WGCP0Only,
		TunnelFactory: func() TunnelManager { return newFakeTunnel() },
		InnerMeshFactory: func() innermesh.Mesh {
			return &recordingMesh{
				inner: innermesh.NewFake(),
				onConfigure: func(c innermesh.Config) {
					recorded = c
					configured = true
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.LoadPersistedBundle(); err != nil {
		t.Fatalf("LoadPersistedBundle: %v", err)
	}

	if _, err := d.SetMode(context.Background(), ipc.SetModeRequest{Mode: string(mode.Combined)}); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	if !configured {
		t.Fatal("SetMode→Combined did not call mesh.Configure; the real Netbird impl would error with \"not configured\" at Connect")
	}
	if recorded.ManagementURL != "http://mgmt.invalid:443" {
		t.Errorf("Configure received ManagementURL=%q, want %q (bundle-derived)",
			recorded.ManagementURL, "http://mgmt.invalid:443")
	}
	if recorded.SetupKey == "" {
		t.Error("Configure received empty SetupKey; bundle.InnerMeshSetup.SetupKey did not flow through")
	}
}

// recordingMesh wraps an inner Mesh, invoking onConfigure on Configure
// so tests can assert the daemon passed the bundle-derived Config to
// the mesh.
type recordingMesh struct {
	inner       innermesh.Mesh
	onConfigure func(innermesh.Config)
}

func (r *recordingMesh) Configure(c innermesh.Config) error {
	if r.onConfigure != nil {
		r.onConfigure(c)
	}
	return r.inner.Configure(c)
}
func (r *recordingMesh) Connect(ctx context.Context) error    { return r.inner.Connect(ctx) }
func (r *recordingMesh) Disconnect(ctx context.Context) error { return r.inner.Disconnect(ctx) }
func (r *recordingMesh) State() innermesh.State               { return r.inner.State() }
func (r *recordingMesh) Stats() (innermesh.Stats, error)      { return r.inner.Stats() }
func (r *recordingMesh) Logs(tail int) []string               { return r.inner.Logs(tail) }
func (r *recordingMesh) Close() error                         { return r.inner.Close() }
