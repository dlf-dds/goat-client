package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// tempSocketPath returns a path short enough to fit in sockaddr_un.sun_path
// on macOS (~104 bytes). t.TempDir() under /var/folders/... is too long.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	var b [4]byte
	_, _ = rand.Read(b[:])
	dir, err := os.MkdirTemp("/tmp", "goat-ipc-")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "ipc-"+hex.EncodeToString(b[:])+".sock")
}

// fakeHandler is a Handler stub that records invocations and lets tests
// inject reply / error values.
type fakeHandler struct {
	mu          sync.Mutex
	importCalls int
	connectCalls int
	disconnectCalls int
	getModeCalls int
	setModeCalls int
	statusReply StatusReply
	importReply ImportBundleReply
	importErr   error
	modeReply   GetModeReply
	setModeRecv SetModeRequest
	setModeReply SetModeReply
}

func (f *fakeHandler) ImportBundle(ctx context.Context, req ImportBundleRequest) (ImportBundleReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importCalls++
	return f.importReply, f.importErr
}

func (f *fakeHandler) GetStatus(ctx context.Context) (StatusReply, error) {
	return f.statusReply, nil
}

func (f *fakeHandler) Connect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	return nil
}

func (f *fakeHandler) Disconnect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls++
	return nil
}

func (f *fakeHandler) GetDiagnostics(ctx context.Context) (DiagnosticsReply, error) {
	return DiagnosticsReply{LogTail: []string{"ok"}}, nil
}

func (f *fakeHandler) GetMode(ctx context.Context) (GetModeReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getModeCalls++
	return f.modeReply, nil
}

func (f *fakeHandler) SetMode(ctx context.Context, req SetModeRequest) (SetModeReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModeCalls++
	f.setModeRecv = req
	return f.setModeReply, nil
}

func startServer(t *testing.T, h Handler) (string, *Server) {
	t.Helper()
	socket := tempSocketPath(t)
	ln, err := Listen(socket)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// trustedUid=0 means accept any local peer in the test (unit tests
	// don't have multiple uids).
	srv := NewServer(h, 0)
	go func() {
		_ = srv.Serve(context.Background(), ln)
	}()
	return socket, srv
}

func TestImportBundleRoundTrip(t *testing.T) {
	h := &fakeHandler{importReply: ImportBundleReply{DeviceID: "dev-1", Site: "kwt-aj-A", EndpointsCount: 3, HasCPDeviceKey: true}}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, err := Dial(socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	cli := newRPCClient(conn)
	defer cli.Close()
	var reply ImportBundleReply
	if err := cli.call(MethodImportBundle, ImportBundleRequest{BundleBytes: []byte{1, 2, 3}}, &reply, 5*time.Second); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if reply.DeviceID != "dev-1" {
		t.Errorf("DeviceID: got %q want %q", reply.DeviceID, "dev-1")
	}
	if h.importCalls != 1 {
		t.Errorf("importCalls: got %d want 1", h.importCalls)
	}
}

func TestGetStatus(t *testing.T) {
	h := &fakeHandler{statusReply: StatusReply{State: WireStateConnected, BytesIn: 1234, BytesOut: 5678}}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()
	var reply StatusReply
	if err := cli.call(MethodGetStatus, EmptyRequest{}, &reply, 5*time.Second); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if reply.State != WireStateConnected {
		t.Errorf("State: got %q want %q", reply.State, WireStateConnected)
	}
	if reply.BytesIn != 1234 {
		t.Errorf("BytesIn: got %d want 1234", reply.BytesIn)
	}
}

func TestConnectDisconnect(t *testing.T) {
	h := &fakeHandler{}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()
	if err := cli.call(MethodConnect, EmptyRequest{}, nil, 5*time.Second); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := cli.call(MethodDisconnect, EmptyRequest{}, nil, 5*time.Second); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if h.connectCalls != 1 || h.disconnectCalls != 1 {
		t.Errorf("connect/disconnect counts: %d/%d", h.connectCalls, h.disconnectCalls)
	}
}

func TestGetSetModeRoundTrip(t *testing.T) {
	h := &fakeHandler{
		modeReply:    GetModeReply{Mode: "wg-cp0-only"},
		setModeReply: SetModeReply{PreviousMode: "wg-cp0-only", Mode: "combined"},
	}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()

	var gm GetModeReply
	if err := cli.call(MethodGetMode, EmptyRequest{}, &gm, 5*time.Second); err != nil {
		t.Fatalf("getMode: %v", err)
	}
	if gm.Mode != "wg-cp0-only" {
		t.Errorf("getMode reply=%q want wg-cp0-only", gm.Mode)
	}

	var sm SetModeReply
	if err := cli.call(MethodSetMode, SetModeRequest{Mode: "combined"}, &sm, 5*time.Second); err != nil {
		t.Fatalf("setMode: %v", err)
	}
	if sm.PreviousMode != "wg-cp0-only" || sm.Mode != "combined" {
		t.Errorf("setMode reply prev=%q new=%q", sm.PreviousMode, sm.Mode)
	}
	if h.setModeRecv.Mode != "combined" {
		t.Errorf("handler received mode=%q want combined", h.setModeRecv.Mode)
	}
}

func TestUnknownMethod(t *testing.T) {
	h := &fakeHandler{}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()
	err := cli.call("nonsense", EmptyRequest{}, nil, 5*time.Second)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestUnauthorizedMutateRejected(t *testing.T) {
	// trustedUid=99999 (no local peer matches) — mutating calls fail,
	// read-only calls succeed.
	h := &fakeHandler{}
	socket := tempSocketPath(t)
	ln, err := Listen(socket)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := NewServer(h, 99999)
	go func() { _ = srv.Serve(context.Background(), ln) }()
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()
	// getStatus is read-only → ok
	var s StatusReply
	if err := cli.call(MethodGetStatus, EmptyRequest{}, &s, 5*time.Second); err != nil {
		t.Fatalf("getStatus: %v", err)
	}
	// connect is mutating → unauthorized
	if err := cli.call(MethodConnect, EmptyRequest{}, nil, 5*time.Second); err == nil {
		t.Fatal("expected unauthorized error for connect")
	}
}
