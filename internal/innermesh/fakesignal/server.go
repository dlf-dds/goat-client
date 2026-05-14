// Package fakesignal wraps netbird's upstream signal-server in a
// test-helper interface. Use it together with internal/innermesh/fakemgmt
// to drive innermesh.Netbird through a full Connect lifecycle without
// any external infrastructure.
//
// We don't roll a fake signal protocol of our own: the upstream
// signal.Server (~210 LOC) is small, embeddable, and what real
// netbird clients talk to. Reusing it means our lifecycle tests
// exercise the same wire protocol the production embed client uses,
// without the cost of writing — and bug-fixing — a re-implementation.
package fakesignal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"

	sigproto "github.com/netbirdio/netbird/shared/signal/proto"
	sigserver "github.com/netbirdio/netbird/signal/server"
)

// Server is the in-process fake netbird signal server. Holds the
// gRPC server + listener so tests can read Addr() and trigger Stop()
// via t.Cleanup.
type Server struct {
	mu       sync.Mutex
	grpcSrv  *grpc.Server
	listener net.Listener
	addr     string
	stopped  bool
}

// Listen builds a fresh signal server, binds on 127.0.0.1:0, registers
// the SignalExchange gRPC service, starts serving on a background
// goroutine, and wires t.Cleanup to stop it. Returns the handle whose
// Addr() the caller passes into fakemgmt.WithSignalURI.
func Listen(t *testing.T) (*Server, error) {
	t.Helper()

	// Upstream signal.NewServer takes an OTel meter for metrics; pass
	// the global no-op meter — we don't surface metrics from tests.
	sigSrv, err := sigserver.NewServer(context.Background(), otel.Meter(""))
	if err != nil {
		return nil, fmt.Errorf("fakesignal: NewServer: %w", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fakesignal: listen: %w", err)
	}

	grpcSrv := grpc.NewServer()
	sigproto.RegisterSignalExchangeServer(grpcSrv, sigSrv)

	s := &Server{
		grpcSrv:  grpcSrv,
		listener: ln,
		addr:     ln.Addr().String(),
	}

	go func() {
		if err := grpcSrv.Serve(ln); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			// Test cleanup races may surface here; surface only as a log.
			// The test itself catches "couldn't reach signal" via the
			// embed client's Connect error path.
			_ = err
		}
	}()
	t.Cleanup(s.Stop)
	return s, nil
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

// Addr returns the host:port the server bound to. Pass into
// fakemgmt.WithSignalURI so the management server tells embed clients
// where to dial for the signal exchange.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
