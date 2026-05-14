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
