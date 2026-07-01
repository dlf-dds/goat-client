package filedrop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server receives dropped files over HTTP and writes them to InboxDir.
// The zero value is not usable — set InboxDir. Auth is fail-closed: a nil
// Authorizer refuses every drop.
type Server struct {
	// InboxDir is where received files land. Created on first receive if
	// absent.
	InboxDir string

	// Auth gates inbound drops by source overlay IP. nil ⇒ deny all.
	Auth Authorizer

	// MaxBytes caps a single file; 0 ⇒ DefaultMaxBytes.
	MaxBytes int64

	// OnReceive, if set, is called after each completed transfer. It must
	// not block for long — it runs on the request goroutine.
	OnReceive func(Received)

	// now is a test seam for the completion timestamp.
	now func() time.Time
}

func (s *Server) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultMaxBytes
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Handler returns the HTTP handler implementing PUT /put/<name>. Exposed
// separately from ListenAndServe so tests (and the daemon) can mount it on
// their own listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(putPrefix, s.handlePut)
	return mux
}

// ListenAndServe binds a TCP listener on addr (host:port; host should be
// the node's overlay IP to keep it mesh-only) and serves until ctx is
// cancelled. It closes the listener it opened before returning.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves inbound drops on an already-bound listener until ctx is
// cancelled, then closes the server. Returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	// ErrServerClosed (from Close on ctx cancel) is the expected shutdown
	// signal, not a fault. Report a real serve error only.
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
		return err
	}
	return nil
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Identity = the connection's source overlay IP, resolved against the
	// Authorizer. Fail closed on a nil Authorizer or an unresolved source.
	srcIP := sourceIP(r.RemoteAddr)
	if s.Auth == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	label, ok := s.Auth.Authorize(srcIP)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	name, err := safeName(strings.TrimPrefix(r.URL.Path, putPrefix))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.ContentLength > s.maxBytes() {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	if err := os.MkdirAll(s.InboxDir, 0o755); err != nil {
		http.Error(w, "inbox unavailable", http.StatusInternalServerError)
		return
	}

	// Stream to a temp file in the inbox, then atomically place it under a
	// collision-free final name. A partial transfer never leaves a
	// half-written file at the destination name.
	tmp, err := os.CreateTemp(s.InboxDir, ".goatdrop-*")
	if err != nil {
		http.Error(w, "inbox unavailable", http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after a successful rename

	limited := http.MaxBytesReader(w, r.Body, s.maxBytes())
	n, err := io.Copy(tmp, limited)
	_ = tmp.Close()
	if err != nil {
		http.Error(w, "transfer failed", http.StatusBadRequest)
		return
	}

	finalPath, err := placeUniquely(s.InboxDir, name, tmpPath)
	if err != nil {
		http.Error(w, "could not store file", http.StatusInternalServerError)
		return
	}

	rec := Received{
		Name:   filepath.Base(finalPath),
		Size:   n,
		From:   label,
		FromIP: srcIP,
		Path:   finalPath,
		At:     s.clock(),
	}
	if s.OnReceive != nil {
		s.OnReceive(rec)
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "stored %s (%d bytes)\n", rec.Name, rec.Size)
}

// sourceIP extracts the IP from a RemoteAddr (host:port), or returns the
// input unchanged if it has no port.
func sourceIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// safeName validates a filename, rejecting empty / dot / separator-bearing
// names so a peer can never write outside the inbox. It rejects rather than
// silently reducing to a base, so a malformed or hostile name surfaces as a
// 400 instead of being quietly rewritten.
func safeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("invalid filename")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("filename must not contain a path separator")
	}
	if strings.HasPrefix(name, ".goatdrop-") {
		return "", errors.New("reserved filename")
	}
	return name, nil
}

// placeUniquely renames tmpPath to dir/name, appending " (N)" before the
// extension on collision so an incoming file never overwrites an existing
// one. Returns the final path.
func placeUniquely(dir, name, tmpPath string) (string, error) {
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
		return final, os.Rename(tmpPath, final)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand, os.Rename(tmpPath, cand)
		}
	}
	return "", errors.New("too many name collisions")
}
