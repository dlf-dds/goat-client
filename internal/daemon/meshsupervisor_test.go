package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// A failed initial inner-mesh bring-up must not be terminal: the mesh
// supervisor retries a wanted-but-StateError mesh until it comes up.

func newSupervisorTestDaemon(t *testing.T, fake *innermesh.Fake) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{
		BundlePath:              filepath.Join(dir, "bundle.cbor"),
		SocketPath:              filepath.Join(dir, "ipc.sock"),
		ConfigPath:              filepath.Join(dir, "config.toml"),
		InitialMode:             mode.NetbirdOnly,
		InnerMeshFactory:        func() innermesh.Mesh { return fake },
		MeshSupervisorInterval:  20 * time.Millisecond,
		MeshSupervisorMax:       100 * time.Millisecond,
		InnerMeshConnectTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestMeshSupervisorRecoversFailedBringUp(t *testing.T) {
	t.Parallel()
	fake := innermesh.NewFake()
	fake.SetFailConnects(2)
	d := newSupervisorTestDaemon(t, fake)

	d.mu.Lock()
	d.currentBundle = &bundle.EnrollmentBundle{}
	d.mu.Unlock()

	// Initial bring-up fails twice: the direct Connect and the
	// supervisor's first retry. The supervisor's second retry succeeds.
	if err := d.Connect(context.Background()); err == nil {
		t.Fatal("Connect should fail while failure injection is armed")
	}
	if fake.State() != innermesh.StateError {
		t.Fatalf("mesh state after failed Connect = %v, want StateError", fake.State())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runMeshSupervisor(ctx)

	deadline := time.After(5 * time.Second)
	for fake.State() != innermesh.StateUp {
		select {
		case <-deadline:
			t.Fatalf("supervisor did not recover the mesh; state=%v", fake.State())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMeshSupervisorRespectsDisable(t *testing.T) {
	t.Parallel()
	fake := innermesh.NewFake()
	fake.SetFailConnects(1)
	d := newSupervisorTestDaemon(t, fake)

	d.mu.Lock()
	d.currentBundle = &bundle.EnrollmentBundle{}
	d.mu.Unlock()

	_ = d.Connect(context.Background()) // fails, meshWanted=true
	if err := d.DisableInnerMesh(context.Background()); err != nil {
		t.Fatalf("DisableInnerMesh: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runMeshSupervisor(ctx)

	// The supervisor must NOT reconnect a mesh the operator turned off.
	time.Sleep(150 * time.Millisecond)
	if st := fake.State(); st == innermesh.StateUp {
		t.Fatalf("supervisor reconnected a disabled mesh (state=%v)", st)
	}
}
