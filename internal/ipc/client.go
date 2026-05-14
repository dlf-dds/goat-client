package ipc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultAddr returns the platform-conventional daemon address. Workers
// can override via flag/env.
//
//   - Linux/macOS: a Unix-domain-socket URL.
//   - Windows: a `\\.\pipe\goat-clientd` named pipe.
//
// The "stub://" scheme returns the in-process stub Client (used during
// GUI development before a goat-clientd is running).
func DefaultAddr() string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\goat-clientd`
	}
	return "unix:///var/run/goat-clientd.sock"
}

// NewClient returns the Client implementation appropriate for `addr`.
//
//   - "" or "stub://..." → the in-process stub (no daemon required).
//   - "unix://<path>" or "/abs/path" → real JSON-RPC over a Unix-domain socket.
//   - "\\.\pipe\<name>" → real JSON-RPC over a Windows named pipe.
//
// The stub remains useful for GUI dev: launching the GUI without a daemon
// running is convenient. Once goat-clientd is up, the GUI passes its
// socket path and the real transport kicks in.
func NewClient(addr string) (Client, error) {
	if addr == "" || strings.HasPrefix(addr, "stub://") {
		return newStubClient(addr), nil
	}
	target := addr
	if strings.HasPrefix(addr, "unix://") {
		target = strings.TrimPrefix(addr, "unix://")
	}
	conn, err := Dial(target)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return newRealClient(conn), nil
}

// rpcClient is the low-level JSON-RPC framing client used internally by
// realClient. It speaks the wire protocol (newline-framed JSON-RPC 2.0)
// against any io.Reader+io.Writer, typically a net.Conn from Dial.
type rpcClient struct {
	conn net.Conn
	mu   sync.Mutex
	enc  *json.Encoder
	rd   *bufio.Reader
	gen  idGenerator
}

func newRPCClient(conn net.Conn) *rpcClient {
	return &rpcClient{
		conn: conn,
		enc:  json.NewEncoder(conn),
		rd:   bufio.NewReader(conn),
	}
}

func (c *rpcClient) Close() error { return c.conn.Close() }

// call invokes a method, marshalling params to JSON and unmarshalling the
// result into out. timeout caps the wait for a response; pass 0 for no
// deadline.
func (c *rpcClient) call(method Method, params, out interface{}, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.gen.Next()
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: rawParams}
	if timeout > 0 {
		_ = c.conn.SetDeadline(time.Now().Add(timeout))
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	line, err := c.rd.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

// realClient is the production Client implementation. Wraps rpcClient
// and translates between wire types (StatusReply, ImportBundleReply,
// DiagnosticsReply) and the GUI-facing Client-interface types
// (StatusInfo, BundleInfo, Diagnostics) that internal/ui depends on.
type realClient struct {
	rpc *rpcClient
}

func newRealClient(conn net.Conn) *realClient {
	return &realClient{rpc: newRPCClient(conn)}
}

func (c *realClient) Close() error { return c.rpc.Close() }

func (c *realClient) ImportBundle(ctx context.Context, raw []byte) (*BundleInfo, error) {
	var reply ImportBundleReply
	if err := c.rpc.call(MethodImportBundle, ImportBundleRequest{BundleBytes: raw}, &reply, 30*time.Second); err != nil {
		return nil, err
	}
	return &BundleInfo{
		IssuedTo:   reply.DeviceID,
		Site:       reply.Site,
		NotBefore:  reply.IssuedAt,
		NotAfter:   reply.ExpiresAt,
		PeerPubKey: base64.StdEncoding.EncodeToString(reply.PeerPubkey),
	}, nil
}

func (c *realClient) GetStatus(ctx context.Context) (*StatusInfo, error) {
	var reply StatusReply
	if err := c.rpc.call(MethodGetStatus, EmptyRequest{}, &reply, 5*time.Second); err != nil {
		return nil, err
	}
	out := &StatusInfo{
		Mode:           reply.Mode,
		State:          mapWireState(reply.State),
		BytesIn:        reply.BytesIn,
		BytesOut:       reply.BytesOut,
		LastHandshake:  reply.LastHandshake,
		PeerPubKey:     base64.StdEncoding.EncodeToString(reply.PeerPubkey),
		Endpoints:      reply.ConfiguredEndpoints,
		BundleImported: reply.BundleLoaded,
		ErrorMessage:   reply.ErrorMessage,
	}
	if reply.BundleLoaded {
		out.Bundle = &BundleInfo{
			IssuedTo:   reply.DeviceID,
			Site:       reply.Site,
			NotAfter:   reply.BundleExpiresAt,
			PeerPubKey: out.PeerPubKey,
			Endpoints:  reply.ConfiguredEndpoints,
		}
	}
	if reply.InnerMesh != nil {
		out.InnerMesh = &InnerMeshInfo{
			State:         mapWireState(reply.InnerMesh.State),
			PeerCount:     reply.InnerMesh.PeerCount,
			BytesIn:       reply.InnerMesh.BytesIn,
			BytesOut:      reply.InnerMesh.BytesOut,
			LastHandshake: reply.InnerMesh.LastHandshake,
		}
	}
	return out, nil
}

func (c *realClient) Connect(ctx context.Context) error {
	return c.rpc.call(MethodConnect, EmptyRequest{}, nil, 30*time.Second)
}

func (c *realClient) Disconnect(ctx context.Context) error {
	return c.rpc.call(MethodDisconnect, EmptyRequest{}, nil, 10*time.Second)
}

func (c *realClient) GetMode(ctx context.Context) (string, error) {
	var reply GetModeReply
	if err := c.rpc.call(MethodGetMode, EmptyRequest{}, &reply, 5*time.Second); err != nil {
		return "", err
	}
	return reply.Mode, nil
}

func (c *realClient) SetMode(ctx context.Context, mode string) (string, error) {
	var reply SetModeReply
	// Mode-switch needs to tear down + bring up subsystems; the daemon
	// budgets ~30s for the verdict-gate. The IPC timeout is generous
	// against that.
	if err := c.rpc.call(MethodSetMode, SetModeRequest{Mode: mode}, &reply, 60*time.Second); err != nil {
		return "", err
	}
	return reply.PreviousMode, nil
}

func (c *realClient) GetDiagnostics(ctx context.Context) (*Diagnostics, error) {
	var reply DiagnosticsReply
	if err := c.rpc.call(MethodGetDiagnostics, EmptyRequest{}, &reply, 5*time.Second); err != nil {
		return nil, err
	}
	return &Diagnostics{
		LogTail:    reply.LogTail,
		LastProbe:  reply.LastConnectAttempt,
		Reachable:  reply.LastError == "",
		ProbeError: reply.LastError,
	}, nil
}

// mapWireState translates the wire-level TunnelState (5 values including
// no-bundle) into the GUI-facing State (4 values). The "no-bundle"
// case appears as Disconnected with BundleImported=false at the GUI
// level — the GUI distinguishes via BundleImported, not State.
func mapWireState(s TunnelState) State {
	switch s {
	case WireStateConnecting:
		return StateConnecting
	case WireStateConnected:
		return StateConnected
	case WireStateError:
		return StateError
	}
	return StateDisconnected
}
