package filedrop

import (
	"context"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowAll authorizes any source as "peer".
var allowAll = AuthorizerFunc(func(ip string) (string, bool) { return "peer", true })

// startServer mounts s.Handler on an httptest server and returns its
// host:port address plus a cleanup.
func startServer(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u.Host
}

func TestSendReceiveRoundTrip(t *testing.T) {
	inbox := t.TempDir()
	var got Received
	s := &Server{InboxDir: inbox, Auth: allowAll, OnReceive: func(r Received) { got = r }}
	addr := startServer(t, s)

	// A source file to send.
	src := filepath.Join(t.TempDir(), "report.txt")
	content := []byte("hello over the mesh\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SendFile(context.Background(), addr, src)
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if res.Name != "report.txt" || res.Size != int64(len(content)) {
		t.Fatalf("send result = %+v", res)
	}

	// It landed in the inbox with identical content.
	landed := filepath.Join(inbox, "report.txt")
	back, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("read landed file: %v", err)
	}
	if string(back) != string(content) {
		t.Fatalf("content mismatch: %q", back)
	}
	// OnReceive fired with the right metadata.
	if got.Name != "report.txt" || got.Size != int64(len(content)) || got.From != "peer" {
		t.Fatalf("OnReceive record = %+v", got)
	}
}

func TestNilAuthorizerFailsClosed(t *testing.T) {
	inbox := t.TempDir()
	s := &Server{InboxDir: inbox} // no Auth
	addr := startServer(t, s)

	_, err := Send(context.Background(), addr, "x.bin", strings.NewReader("data"), 4)
	if err == nil {
		t.Fatal("expected refusal with a nil Authorizer")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	// Nothing written.
	entries, _ := os.ReadDir(inbox)
	if len(entries) != 0 {
		t.Fatalf("inbox has %d entries, want 0", len(entries))
	}
}

func TestUnauthorizedSourceRejected(t *testing.T) {
	inbox := t.TempDir()
	// Authorize only an IP that is not the test client's loopback source.
	s := &Server{InboxDir: inbox, Auth: AuthorizerFunc(func(ip string) (string, bool) {
		return "", ip == "100.64.0.42"
	})}
	addr := startServer(t, s)

	if _, err := Send(context.Background(), addr, "x.bin", strings.NewReader("data"), 4); err == nil {
		t.Fatal("expected refusal for an unauthorized source")
	}
	if entries, _ := os.ReadDir(inbox); len(entries) != 0 {
		t.Fatalf("inbox has %d entries, want 0", len(entries))
	}
}

func TestRejectsTraversalNames(t *testing.T) {
	inbox := t.TempDir()
	s := &Server{InboxDir: inbox, Auth: allowAll}
	addr := startServer(t, s)

	for _, bad := range []string{"..", "../escape", "a/b", `..\win`} {
		if _, err := Send(context.Background(), addr, bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("name %q was accepted, want rejection", bad)
		}
	}
	// Confirm nothing escaped the inbox's parent.
	if _, err := os.Stat(filepath.Join(filepath.Dir(inbox), "escape")); err == nil {
		t.Fatal("a traversal write escaped the inbox")
	}
}

func TestCollisionGetsSuffixed(t *testing.T) {
	inbox := t.TempDir()
	s := &Server{InboxDir: inbox, Auth: allowAll}
	addr := startServer(t, s)

	for i := 0; i < 2; i++ {
		if _, err := Send(context.Background(), addr, "dup.txt", strings.NewReader("v"), 1); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(inbox, "dup.txt")); err != nil {
		t.Fatal("first file should keep its name")
	}
	if _, err := os.Stat(filepath.Join(inbox, "dup (1).txt")); err != nil {
		t.Fatal("second file should be suffixed to avoid overwrite")
	}
}

func TestSizeCapRejects(t *testing.T) {
	inbox := t.TempDir()
	s := &Server{InboxDir: inbox, Auth: allowAll, MaxBytes: 8}
	addr := startServer(t, s)

	if _, err := Send(context.Background(), addr, "big.bin", strings.NewReader("way too many bytes"), 18); err == nil {
		t.Fatal("expected refusal over the size cap")
	}
	if entries, _ := os.ReadDir(inbox); len(entries) != 0 {
		t.Fatalf("inbox has %d entries after an over-cap send, want 0", len(entries))
	}
}

func TestSafeName(t *testing.T) {
	ok := map[string]string{
		"a.txt":         "a.txt",
		"  spaced.pdf ": "spaced.pdf",
		"weird name.7z": "weird name.7z",
	}
	for in, want := range ok {
		got, err := safeName(in)
		if err != nil || got != want {
			t.Errorf("safeName(%q) = %q,%v want %q,nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, ".goatdrop-x"} {
		if _, err := safeName(bad); err == nil {
			t.Errorf("safeName(%q) accepted, want error", bad)
		}
	}
}

func TestServeRoundTrip(t *testing.T) {
	inbox := t.TempDir()
	s := &Server{InboxDir: inbox, Auth: allowAll}

	// Pre-bind the listener so the address is live before Serve runs (no
	// race); queued connections are served once Serve accepts them.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx, ln); close(done) }()

	if _, err := Send(context.Background(), addr, "viaserve.txt", strings.NewReader("ok"), 2); err != nil {
		t.Fatalf("send via Serve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(inbox, "viaserve.txt")); err != nil {
		t.Fatalf("file not stored via Serve: %v", err)
	}
	cancel()
	<-done
}
