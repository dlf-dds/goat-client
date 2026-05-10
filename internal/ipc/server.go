package ipc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
)

// Server accepts inbound IPC connections and dispatches each to a
// Dispatcher. The transport (Unix domain socket, Windows named pipe) is
// supplied by the caller as a net.Listener via Listen — the per-OS
// listener-construction lives in transport_unix.go / transport_windows.go.
type Server struct {
	handler    Handler
	trustedUid uint32

	mu     sync.Mutex
	ln     net.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewServer creates a Server. trustedUid is the only uid permitted to
// invoke mutating methods (importBundle, connect, disconnect). Passing 0
// disables uid enforcement (test-only — production callers should pass
// os.Getuid()).
func NewServer(h Handler, trustedUid uint32) *Server {
	return &Server{handler: h, trustedUid: trustedUid}
}

// Serve takes ownership of the listener and accepts connections until
// ctx is cancelled or the listener errors. Each connection runs in its
// own goroutine.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.ln = ln
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				return nil //nolint:nilerr // Accept failure on cancelled ctx is graceful shutdown
			}
			if errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				return nil //nolint:nilerr // listener closed by Stop() is graceful shutdown
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			creds, err := peerCreds(conn)
			if err != nil {
				log.Printf("ipc: peer creds lookup failed: %v", err)
				return
			}
			d := NewDispatcher(s.handler, creds, s.trustedUid)
			if err := d.Serve(ctx, conn, conn); err != nil {
				log.Printf("ipc: dispatcher exited: %v", err)
			}
		}()
	}
}

// Close stops accepting new connections and waits for in-flight
// dispatchers to finish.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	s.wg.Wait()
	return nil
}
