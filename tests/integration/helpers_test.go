//go:build integration || realprotocol || mobile_realprotocol

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/ipc"
)

// daemonBinary holds the path to the goat-clientd binary built once
// per test process. Lazily built on first use.
var (
	daemonBinaryOnce sync.Once
	daemonBinaryPath string
	daemonBinaryErr  error
)

// buildDaemon compiles cmd/goat-clientd into a temp file. Cached across
// tests in the same `go test` invocation.
func buildDaemon(t *testing.T) string {
	t.Helper()
	daemonBinaryOnce.Do(func() {
		repoRoot, err := findRepoRoot()
		if err != nil {
			daemonBinaryErr = err
			return
		}
		dir, err := os.MkdirTemp("", "goat-clientd-bin-")
		if err != nil {
			daemonBinaryErr = err
			return
		}
		bin := filepath.Join(dir, "goat-clientd")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/goat-clientd")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			daemonBinaryErr = errors.New("go build failed: " + string(out))
			return
		}
		daemonBinaryPath = bin
	})
	if daemonBinaryErr != nil {
		t.Fatalf("build daemon: %v", daemonBinaryErr)
	}
	return daemonBinaryPath
}

// findRepoRoot walks up from the test's runtime cwd looking for go.mod.
func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", errors.New("go.mod not found from " + wd)
		}
		wd = parent
	}
}

// fixture pairs a signed bundle's wire bytes with the trust-root PEM the
// daemon must be configured with to accept it.
type fixture struct {
	BundleBytes     []byte
	TrustRootsPEM   []byte
	BundlePath      string // file containing BundleBytes
	TrustRootsPath  string // file containing TrustRootsPEM
	SocketPath      string // short Unix socket path the daemon listens on
	BundleStatePath string // path the daemon persists imported bundles to
	Site            string
	DeviceID        string
}

// newFixture mints a fresh ECDSA P-256 trust root + a signed
// EnrollmentBundle valid from now-1h to now+24h, writes both to disk
// under a short tempdir (sun_path-safe on macOS), and returns the paths
// the daemon needs.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := shortTempDir(t)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa P-256: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	trustPath := filepath.Join(dir, "trust-roots.pem")
	if err := os.WriteFile(trustPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write trust roots: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	b := &bundle.EnrollmentBundle{
		Version:    bundle.Version,
		DeviceID:   "integration-device",
		PeerPubkey: bytes32("integration-peer-pubkey-aaaaaaaa"),
		ACLGroups:  []string{"integration"},
		Site:       "integration-lab",
		KnownEndpoints: []bundle.KnownEndpoint{{
			Addr:     "203.0.113.10:51820",
			Pubkey:   bytes32("integration-relay-pubkey-bbbbbbbb"),
			Kind:     bundle.KindRelay,
			MeshAddr: "198.18.0.10",
		}},
		IssuedAt:           now,
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(24 * time.Hour),
		Nonce:              bytes16("integration-nce"),
		CAID:               "integration-test-ca",
	}
	if err := signBundle(b, priv); err != nil {
		t.Fatalf("sign bundle: %v", err)
	}
	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	bundlePath := filepath.Join(dir, "bundle.cbor")
	// We do NOT pre-write the bundle to bundlePath — the daemon expects
	// the path NOT to exist on first start (it persists post-import).
	// We give it a path inside tempdir so the persisted file is cleaned
	// up by t.Cleanup.

	return &fixture{
		BundleBytes:     wire,
		TrustRootsPEM:   pemBytes,
		BundlePath:      filepath.Join(dir, "bundle-input.cbor"),
		TrustRootsPath:  trustPath,
		SocketPath:      filepath.Join(dir, "ipc.sock"),
		BundleStatePath: bundlePath,
		Site:            b.Site,
		DeviceID:        b.DeviceID,
	}
}

// signBundle signs `b` with priv using ECDSA P-256 over SHA-256 of the
// canonical CBOR payload — mirrors the offline CA's sign-flow byte-for-
// byte (Block 79 cutover) and the test-helper Sign defined in
// internal/bundle/bundle_test.go (which is package-local and not
// importable here).
func signBundle(b *bundle.EnrollmentBundle, priv *ecdsa.PrivateKey) error {
	payload, err := b.Signable()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		return err
	}
	if len(sig) == 0 {
		return errors.New("signature is empty")
	}
	b.Signature = sig
	return nil
}

// daemonProc wraps a running daemon subprocess and a connected Client.
type daemonProc struct {
	cmd    *exec.Cmd
	fix    *fixture
	client ipc.Client
	stderr *captureBuffer
}

// startDaemon launches a daemon subprocess wired to fix's socket + trust
// roots + bundle-state path, then connects an ipc.Client to it. Process
// + connection are torn down in t.Cleanup.
func startDaemon(t *testing.T, fix *fixture) *daemonProc {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("integration tests use Unix sockets; Windows named-pipe transport via Track A is exercised by internal/ipc/ipc_test.go")
	}
	bin := buildDaemon(t)

	cmd := exec.Command(bin,
		"--socket", fix.SocketPath,
		"--trust-roots", fix.TrustRootsPath,
		"--bundle", fix.BundleStatePath,
	)
	stderr := &captureBuffer{}
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
		if t.Failed() {
			t.Logf("daemon stderr/stdout:\n%s", stderr.String())
		}
	})

	// Poll for the socket to appear; daemon binds it shortly after start.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(fix.SocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	client, err := ipc.NewClient("unix://" + fix.SocketPath)
	if err != nil {
		t.Fatalf("dial daemon socket: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = client.Close() })

	return &daemonProc{cmd: cmd, fix: fix, client: client, stderr: stderr}
}

// shortTempDir returns a tempdir with a path short enough to fit in
// sockaddr_un.sun_path on macOS (~104 bytes). t.TempDir() under
// /var/folders/... overflows for long test names.
func shortTempDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().Format(time.RFC3339Nano)))
	name := "g-" + hex.EncodeToString(sum[:4])
	dir := filepath.Join("/tmp", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// captureBuffer is a tiny mutex-guarded buffer; exec.Cmd writes from a
// goroutine, the test reads from another, so a plain bytes.Buffer would
// race per the os/exec docs.
type captureBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *captureBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// testCtx returns a fresh context for one IPC call.
func testCtx() context.Context { return context.Background() }

// bytes32 returns a 32-byte slice padded from s. Bundle pubkey/relay-key
// fields require exactly 32 bytes (Ed25519 / WG public key sizes); the
// daemon's bundle parser does not enforce length but the wire schema
// expects it.
func bytes32(s string) []byte { return padBytes(s, 32) }

// bytes16 returns a 16-byte slice padded from s.
func bytes16(s string) []byte { return padBytes(s, 16) }

func padBytes(s string, n int) []byte {
	out := make([]byte, n)
	copy(out, s)
	return out
}
