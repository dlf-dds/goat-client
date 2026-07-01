package daemon

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	fd "github.com/dlf-dds/goat-client/internal/filedrop"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/mode"
)

func TestAuthorizerAdmitsKnownPeerDeniesUnknown(t *testing.T) {
	d := newTestDaemon(t, mode.NetbirdOnly)
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}
	auth := d.authorizer()

	label, ok := auth.Authorize("100.92.0.11") // a Fake mesh peer
	if !ok {
		t.Fatal("known peer IP was denied")
	}
	if label != "alpha.goat" {
		t.Errorf("label = %q, want alpha.goat", label)
	}
	if _, ok := auth.Authorize("203.0.113.9"); ok {
		t.Fatal("unknown source IP was admitted")
	}
}

func TestAuthorizerDeniesWhenMeshDown(t *testing.T) {
	d := newTestDaemon(t, mode.NetbirdOnly) // mesh constructed but not Connect'd
	if _, ok := d.authorizer().Authorize("100.92.0.11"); ok {
		t.Fatal("authorizer admitted a peer while the mesh is down (fail-closed expected)")
	}
}

func TestReceivedRingNewestFirstAndEvicts(t *testing.T) {
	r := newReceivedRing(3)
	for i := 1; i <= 5; i++ {
		r.add(fd.Received{Name: string(rune('a' + i - 1)), Size: int64(i)})
	}
	got := r.list()
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (capacity)", len(got))
	}
	// Newest first: last added was size 5, then 4, then 3.
	if got[0].Size != 5 || got[1].Size != 4 || got[2].Size != 3 {
		t.Fatalf("order wrong: %+v", got)
	}
}

func TestGetIncomingFilesReturnsRing(t *testing.T) {
	d := newTestDaemon(t, mode.NetbirdOnly)
	d.received.add(fd.Received{Name: "a.txt", Size: 10, From: "alpha.goat", FromIP: "100.92.0.11"})
	d.received.add(fd.Received{Name: "b.bin", Size: 20, From: "bravo.goat"})

	reply, err := d.GetIncomingFiles(context.Background())
	if err != nil {
		t.Fatalf("GetIncomingFiles: %v", err)
	}
	if len(reply.Files) != 2 {
		t.Fatalf("files=%d want 2", len(reply.Files))
	}
	// Newest first.
	if reply.Files[0].Name != "b.bin" || reply.Files[1].Name != "a.txt" {
		t.Fatalf("order/content wrong: %+v", reply.Files)
	}
	if reply.Files[1].From != "alpha.goat" || reply.Files[1].FromIP != "100.92.0.11" {
		t.Errorf("metadata not carried: %+v", reply.Files[1])
	}
}

// fakeFileServer records the reconcile loop's ListenAndServe call and blocks
// until its context is cancelled (mirroring a real server).
type fakeFileServer struct {
	mu      sync.Mutex
	started bool
	addr    string
	ctx     context.Context
}

func (f *fakeFileServer) ListenAndServe(ctx context.Context, addr string) error {
	f.mu.Lock()
	f.started = true
	f.addr = addr
	f.ctx = ctx
	f.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (f *fakeFileServer) snap() (bool, string, context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.addr, f.ctx
}

func TestFileServerReconcileLifecycle(t *testing.T) {
	ffs := &fakeFileServer{}
	dir := t.TempDir()
	d, err := New(Config{
		BundlePath:                filepath.Join(dir, "bundle.cbor"),
		SocketPath:                filepath.Join(dir, "ipc.sock"),
		ConfigPath:                filepath.Join(dir, "config.toml"),
		InitialMode:               mode.NetbirdOnly,
		InnerMeshFactory:          func() innermesh.Mesh { return innermesh.NewFake() },
		PeerConnReconcileInterval: 20 * time.Millisecond,
		FileServerFactory: func(inbox string, auth fd.Authorizer, onRecv func(fd.Received)) fileServer {
			return ffs
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.mesh.Connect(context.Background()); err != nil {
		t.Fatalf("mesh connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runFileServer(ctx)

	// Server starts bound to the Fake's local tunnel IP + the filedrop port.
	wantAddr := net.JoinHostPort("100.92.0.1", strconv.Itoa(fd.DefaultPort))
	waitCond(t, 3*time.Second, func() bool {
		started, addr, _ := ffs.snap()
		return started && addr == wantAddr
	})

	// Bring the mesh down → the server's context is cancelled.
	if err := d.mesh.Disconnect(context.Background()); err != nil {
		t.Fatalf("mesh disconnect: %v", err)
	}
	waitCond(t, 3*time.Second, func() bool {
		_, _, sctx := ffs.snap()
		return sctx != nil && sctx.Err() != nil
	})
}
