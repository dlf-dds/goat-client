package fakemgmt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"

	mgmtproto "github.com/netbirdio/netbird/shared/management/proto"
)

// Server is the in-process fake netbird management server. Implements
// the proto.ManagementServiceServer interface by embedding
// UnimplementedManagementServiceServer (so methods we haven't yet
// landed return Unimplemented rather than panic) plus a small set of
// real implementations.
//
// Construct via Listen(t) for tests, or NewServer + Serve for
// non-test callers (the headless smoke in M4).
type Server struct {
	mgmtproto.UnimplementedManagementServiceServer

	// WG keypair the fake mgmt-server uses to sign + decrypt
	// EncryptedMessage payloads. Generated fresh per Listen call so
	// each test gets an isolated key.
	wgKey wgtypes.Key

	// signalURI is the host:port the fake tells embedded clients to
	// dial for the netbird signal exchange (returned in
	// LoginResponse.NetbirdConfig.Signal.Uri). Empty when callers only
	// exercise GetServerKey/IsHealthy and don't drive Login.
	signalURI string

	mu       sync.Mutex
	grpcSrv  *grpc.Server
	listener net.Listener
	addr     string
	stopped  bool
}

// Option configures a Server at construction. Use WithSignalURI to
// point Login responses at a fakesignal listener.
type Option func(*Server)

// WithSignalURI sets the host:port that Login responses will tell
// clients to use for the signal-exchange RPC. Required for any test
// that drives the embed client through Connect (the engine's signal
// dial would otherwise fail). Pass the address from
// fakesignal.Listen(t).
func WithSignalURI(uri string) Option {
	return func(s *Server) { s.signalURI = uri }
}

// NewServer constructs a Server with a fresh WG keypair. The server
// is not started — call Serve(ln) or use Listen(t) for the test
// helper path.
func NewServer(opts ...Option) (*Server, error) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fakemgmt: generate wg key: %w", err)
	}
	s := &Server{wgKey: key}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Serve binds + serves the fake's gRPC service on ln. Returns when
// the listener is closed or Stop is called. Safe to call from a
// goroutine. Listen() captures ln.Addr() before invoking Serve in
// a goroutine, so Server.Addr() is readable as soon as Listen
// returns — Serve() doesn't have to win the goroutine race.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return errors.New("fakemgmt: server stopped")
	}
	srv := grpc.NewServer()
	mgmtproto.RegisterManagementServiceServer(srv, s)
	s.grpcSrv = srv
	s.listener = ln
	if s.addr == "" {
		s.addr = ln.Addr().String()
	}
	s.mu.Unlock()
	return srv.Serve(ln)
}

// Stop gracefully stops the gRPC server + closes the listener.
// Idempotent.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.grpcSrv != nil {
		s.grpcSrv.GracefulStop()
	}
}

// Addr returns the address the server bound to (host:port). Empty
// before Serve. Used by tests to derive the ManagementURL to pass
// into innermesh.Config.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// PublicKey returns the fake's WG public key. Used by the test
// itself (e.g., for inspecting that GetServerKey responds with this
// key) and exposed for symmetry with the real netbird mgmt-server.
func (s *Server) PublicKey() wgtypes.Key {
	return s.wgKey.PublicKey()
}

// Listen is the test-helper constructor: builds a fresh Server, binds
// on 127.0.0.1:0, registers the gRPC service, starts serving on a
// background goroutine, and returns the handle. t.Cleanup wires the
// stop so the listener is released at test end.
//
// Caller derives the ManagementURL from s.Addr() — typically
// "http://" + s.Addr() (gRPC over plaintext for the in-process
// hermetic case; the real fake mgmt-server gates TLS off via a flag
// the embed client honors).
func Listen(t *testing.T, opts ...Option) (*Server, error) {
	t.Helper()
	srv, err := NewServer(opts...)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fakemgmt: listen: %w", err)
	}
	// Capture the bound address before starting Serve in a goroutine
	// so callers don't race against Serve setting s.addr.
	srv.mu.Lock()
	srv.addr = ln.Addr().String()
	srv.mu.Unlock()
	go func() {
		_ = srv.Serve(ln)
	}()
	t.Cleanup(srv.Stop)
	return srv, nil
}

// --- ManagementServiceServer implementations ---

// GetServerKey returns the fake's WG public key wrapped in a
// ServerKeyResponse. Every embed startup calls this so the client
// can build the encryption envelope for Login + Sync.
func (s *Server) GetServerKey(_ context.Context, _ *mgmtproto.Empty) (*mgmtproto.ServerKeyResponse, error) {
	pub := s.wgKey.PublicKey().String()
	return &mgmtproto.ServerKeyResponse{
		Key: pub,
		// ExpiresAt is left zero — the embed client honors zero as
		// "no rotation," which is what the fake wants.
	}, nil
}

// IsHealthy is the netbird mgmt-server health-check (lowercase per
// the .proto's rpc definition; the generated Go uses the
// IsHealthy() name on the server interface anyway).
func (s *Server) IsHealthy(_ context.Context, _ *mgmtproto.Empty) (*mgmtproto.Empty, error) {
	return &mgmtproto.Empty{}, nil
}

