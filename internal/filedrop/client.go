package filedrop

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// SendResult summarizes a completed outbound drop.
type SendResult struct {
	Name string
	Size int64
}

// SendFile drops the file at path to a peer's filedrop Server at addr
// (host:port on the peer's overlay IP; port 0-style callers should use
// PeerAddr). The remote stores it under the file's base name.
func SendFile(ctx context.Context, addr, path string) (SendResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return SendResult{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return SendResult{}, err
	}
	if info.IsDir() {
		return SendResult{}, fmt.Errorf("filedrop: %s is a directory", path)
	}
	return Send(ctx, addr, filepath.Base(path), f, info.Size())
}

// Send streams body (of the given size) to a peer's filedrop Server at
// addr, stored remotely under name. A non-2xx response is returned as an
// error carrying the server's message.
func Send(ctx context.Context, addr, name string, body io.Reader, size int64) (SendResult, error) {
	clean, err := safeName(name)
	if err != nil {
		return SendResult{}, err
	}

	u := url.URL{Scheme: "http", Host: addr, Path: putPrefix + clean}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), body)
	if err != nil {
		return SendResult{}, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		return SendResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return SendResult{}, fmt.Errorf("filedrop: peer refused (%s): %s", resp.Status, string(msg))
	}
	return SendResult{Name: clean, Size: size}, nil
}

// PeerAddr builds a filedrop server address from a peer overlay IP and an
// optional port (0 ⇒ DefaultPort).
func PeerAddr(ip string, port int) string {
	if port <= 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(ip, strconv.Itoa(port))
}

// httpClient is shared by senders; no redirect-following, a generous
// timeout for large files (the tunnel, not the app, bounds throughput).
var httpClient = &http.Client{
	Timeout: 30 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
