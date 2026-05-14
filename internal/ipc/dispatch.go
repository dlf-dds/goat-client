package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

// Wire envelope (JSON-RPC 2.0 line-framed). One request + reply per line so
// the transport can be debugged with `nc -U /path/to/socket` without
// extra tooling.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  Method          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes plus daemon-specific extensions
// (-32000…-32099 reserved for impl per JSON-RPC 2.0 §5.1).
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
	errCodeUnauthorized   = -32001
)

// Dispatcher routes a single connection's JSON-RPC traffic to the daemon's
// Handler. One Dispatcher per accepted connection — mutex-free because the
// transport guarantees a single goroutine drives a single connection.
type Dispatcher struct {
	h     Handler
	creds PeerCreds
	// trustedUid is the uid that may invoke mutating methods. Read-only
	// methods are allowed for any local peer.
	trustedUid uint32
}

// NewDispatcher binds a Handler to a peer-authenticated connection.
func NewDispatcher(h Handler, creds PeerCreds, trustedUid uint32) *Dispatcher {
	return &Dispatcher{h: h, creds: creds, trustedUid: trustedUid}
}

// Serve drains JSON-RPC requests from r, dispatches them to the Handler,
// and writes responses to w. Returns when r reaches EOF or ctx is
// cancelled. Each request is line-delimited; responses likewise.
func (d *Dispatcher) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// 1 MiB max line size — bundles up to ~512 KiB are comfortable but
	// nothing larger should ever cross the IPC boundary; oversized requests
	// would be a sign of a misbehaving client.
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: errCodeParse, Message: err.Error()}})
			continue
		}
		resp := d.handle(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// handle executes one request. Errors are returned in-band as rpcError;
// the only way handle returns a non-nil error is for a transport-level
// problem (which propagates out of Serve).
func (d *Dispatcher) handle(ctx context.Context, req *rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{Code: errCodeInvalidRequest, Message: "missing jsonrpc:2.0 or method"}
		return resp
	}
	if isMutating(req.Method) && !d.authorizedForMutate() {
		resp.Error = &rpcError{Code: errCodeUnauthorized, Message: ErrUnauthorized.Error()}
		return resp
	}
	switch req.Method {
	case MethodImportBundle:
		var p ImportBundleRequest
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
			return resp
		}
		out, err := d.h.ImportBundle(ctx, p)
		fillResult(&resp, out, err)
	case MethodGetStatus:
		out, err := d.h.GetStatus(ctx)
		fillResult(&resp, out, err)
	case MethodConnect:
		err := d.h.Connect(ctx)
		fillResult(&resp, EmptyReply{}, err)
	case MethodDisconnect:
		err := d.h.Disconnect(ctx)
		fillResult(&resp, EmptyReply{}, err)
	case MethodGetDiagnostics:
		out, err := d.h.GetDiagnostics(ctx)
		fillResult(&resp, out, err)
	case MethodGetMode:
		out, err := d.h.GetMode(ctx)
		fillResult(&resp, out, err)
	case MethodSetMode:
		var p SetModeRequest
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: errCodeInvalidParams, Message: err.Error()}
			return resp
		}
		out, err := d.h.SetMode(ctx, p)
		fillResult(&resp, out, err)
	default:
		resp.Error = &rpcError{Code: errCodeMethodNotFound, Message: ErrUnknownMethod.Error()}
	}
	return resp
}

func (d *Dispatcher) authorizedForMutate() bool {
	// trustedUid==0 means "any local peer authorized" — we use this only
	// in tests; production daemons set it to the owning uid.
	return d.trustedUid == 0 || d.creds.Uid == d.trustedUid
}

func isMutating(m Method) bool {
	switch m {
	case MethodImportBundle, MethodConnect, MethodDisconnect, MethodSetMode:
		return true
	}
	return false
}

func fillResult(resp *rpcResponse, out interface{}, err error) {
	if err != nil {
		resp.Error = &rpcError{Code: errCodeInternal, Message: err.Error()}
		return
	}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		resp.Error = &rpcError{Code: errCodeInternal, Message: mErr.Error()}
		return
	}
	resp.Result = b
}

// requestID returns a monotonically increasing JSON-RPC request id for the
// client side. JSON-RPC IDs can be any value; we use a uint64 counter for
// simplicity.
type idGenerator struct{ next atomic.Uint64 }

func (g *idGenerator) Next() json.RawMessage {
	n := g.next.Add(1)
	b, _ := json.Marshal(n)
	return b
}
