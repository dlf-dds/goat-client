package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// tempSocketPath returns a path short enough to fit in sockaddr_un.sun_path
// on macOS (~104 bytes). t.TempDir() under /var/folders/... is too long.
//
// Skips on Windows: the daemon uses a named-pipe transport (see
// internal/ipc/transport_windows.go); the tests in this file exercise
// the Unix-socket transport only. Block 76 GAP #3 (cross-platform PR
// gate) surfaced this gap when test execution started on Windows
// runners — adding a parallel named-pipe test suite is tracked
// separately.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-socket transport tests; Windows uses named pipes (covered by tests/integration once added)")
	}
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
	mu              sync.Mutex
	importCalls     int
	connectCalls    int
	disconnectCalls int
	getModeCalls    int
	setModeCalls    int
	statusReply     StatusReply
	importReply     ImportBundleReply
	importErr       error
	modeReply       GetModeReply
	setModeRecv     SetModeRequest
	setModeReply    SetModeReply

	// v0.2 inner-mesh-direct fields.
	innerStatusReply      InnerMeshSnapshot
	setInnerProfileCalls  int
	setInnerProfileRecv   SetInnerMeshProfileRequest
	enableInnerCalls      int
	disableInnerCalls     int
	innerDiagnosticsReply InnerMeshDiagnosticsReply
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

func (f *fakeHandler) GetInnerMeshStatus(ctx context.Context) (InnerMeshSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.innerStatusReply, nil
}

func (f *fakeHandler) SetInnerMeshProfile(ctx context.Context, req SetInnerMeshProfileRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setInnerProfileCalls++
	f.setInnerProfileRecv = req
	return nil
}

func (f *fakeHandler) EnableInnerMesh(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableInnerCalls++
	return nil
}

func (f *fakeHandler) DisableInnerMesh(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableInnerCalls++
	return nil
}

func (f *fakeHandler) GetInnerMeshDiagnostics(ctx context.Context) (InnerMeshDiagnosticsReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.innerDiagnosticsReply, nil
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

// TestV0_2InnerMeshMethods drives the five v0.2 inner-mesh IPC
// methods (getInnerMeshStatus / setInnerMeshProfile /
// enableInnerMesh / disableInnerMesh / getInnerMeshDiagnostics)
// end-to-end through the dispatcher. Validates each method reaches
// the Handler, the wire types round-trip cleanly (in particular
// SetInnerMeshProfileRequest's []byte fields base64 through JSON),
// and the mutating methods are flagged in isMutating.
func TestV0_2InnerMeshMethods(t *testing.T) {
	h := &fakeHandler{
		innerStatusReply: InnerMeshSnapshot{
			State: WireStateConnected, PeerCount: 3, BytesIn: 4096, BytesOut: 1024,
		},
		innerDiagnosticsReply: InnerMeshDiagnosticsReply{
			LogTail: []string{"alpha", "beta"},
		},
	}
	socket, srv := startServer(t, h)
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()

	// getInnerMeshStatus
	var ims InnerMeshSnapshot
	if err := cli.call(MethodGetInnerMeshStatus, EmptyRequest{}, &ims, 5*time.Second); err != nil {
		t.Fatalf("getInnerMeshStatus: %v", err)
	}
	if ims.State != WireStateConnected || ims.PeerCount != 3 {
		t.Errorf("getInnerMeshStatus reply mismatch: %+v", ims)
	}

	// setInnerMeshProfile — including base64-roundtrip on []byte fields
	req := SetInnerMeshProfileRequest{
		ManagementURL: "https://mgmt.example.internal:33073",
		SetupKey:      "TEST-SETUP-KEY",
		MobileCert:    []byte("pem-bytes-stub"),
		PreSharedKey:  []byte("32-byte-psk-stub-fill-to-32-bytes"),
	}
	if err := cli.call(MethodSetInnerMeshProfile, req, nil, 5*time.Second); err != nil {
		t.Fatalf("setInnerMeshProfile: %v", err)
	}
	if h.setInnerProfileCalls != 1 {
		t.Errorf("setInnerMeshProfile calls: got %d want 1", h.setInnerProfileCalls)
	}
	if h.setInnerProfileRecv.ManagementURL != req.ManagementURL {
		t.Errorf("ManagementURL round-trip: got %q want %q",
			h.setInnerProfileRecv.ManagementURL, req.ManagementURL)
	}
	if string(h.setInnerProfileRecv.MobileCert) != "pem-bytes-stub" {
		t.Errorf("MobileCert round-trip: got %q want pem-bytes-stub",
			string(h.setInnerProfileRecv.MobileCert))
	}

	// enableInnerMesh / disableInnerMesh
	if err := cli.call(MethodEnableInnerMesh, EmptyRequest{}, nil, 5*time.Second); err != nil {
		t.Fatalf("enableInnerMesh: %v", err)
	}
	if err := cli.call(MethodDisableInnerMesh, EmptyRequest{}, nil, 5*time.Second); err != nil {
		t.Fatalf("disableInnerMesh: %v", err)
	}
	if h.enableInnerCalls != 1 || h.disableInnerCalls != 1 {
		t.Errorf("enable/disable counts: %d/%d", h.enableInnerCalls, h.disableInnerCalls)
	}

	// getInnerMeshDiagnostics
	var idr InnerMeshDiagnosticsReply
	if err := cli.call(MethodGetInnerMeshDiagnostics, EmptyRequest{}, &idr, 5*time.Second); err != nil {
		t.Fatalf("getInnerMeshDiagnostics: %v", err)
	}
	if len(idr.LogTail) != 2 || idr.LogTail[0] != "alpha" {
		t.Errorf("InnerMeshDiagnostics.LogTail: %v want [alpha beta]", idr.LogTail)
	}
}

// TestV0_2InnerMeshMutatingMethodsRequireAuth confirms the three
// mutating v0.2 inner-mesh methods (setInnerMeshProfile,
// enableInnerMesh, disableInnerMesh) are flagged in isMutating() so
// the uid-auth check gates them. Read-only methods
// (getInnerMeshStatus, getInnerMeshDiagnostics) stay accessible.
func TestV0_2InnerMeshMutatingMethodsRequireAuth(t *testing.T) {
	h := &fakeHandler{}
	socket := tempSocketPath(t)
	ln, err := Listen(socket)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := NewServer(h, 99999) // no local peer matches
	go func() { _ = srv.Serve(context.Background(), ln) }()
	defer srv.Close()
	conn, _ := Dial(socket)
	cli := newRPCClient(conn)
	defer cli.Close()

	// mutating methods → rejected
	mutating := []struct {
		method Method
		params interface{}
	}{
		{MethodSetInnerMeshProfile, SetInnerMeshProfileRequest{ManagementURL: "https://x", SetupKey: "k"}},
		{MethodEnableInnerMesh, EmptyRequest{}},
		{MethodDisableInnerMesh, EmptyRequest{}},
	}
	for _, tc := range mutating {
		if err := cli.call(tc.method, tc.params, nil, 5*time.Second); err == nil {
			t.Errorf("%s: expected unauthorized error, got nil", tc.method)
		}
	}

	// read-only methods → accepted
	var ims InnerMeshSnapshot
	if err := cli.call(MethodGetInnerMeshStatus, EmptyRequest{}, &ims, 5*time.Second); err != nil {
		t.Errorf("getInnerMeshStatus under untrusted peer: %v want success", err)
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
