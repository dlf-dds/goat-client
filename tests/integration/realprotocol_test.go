//go:build realprotocol

package integration

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// TestRealProtocol_BundleImportAndHandshake brings up the real wg-cp0
// outer tunnel against an endpoint baked into the bundle's
// KnownEndpoints, then probes a target IP through the tunnel to assert
// reachability — not just handshake completion. Sibling to the in-process
// integration tests; same harness shape, real bundle + real driver.
//
// Required env (any unset → t.Skip):
//
//   - GOAT_LAB_BUNDLE_PATH        path to a CBOR-encoded offline-CA bundle
//   - GOAT_LAB_TRUST_ROOTS_PATH   PEM with the Ed25519 pubkey that signed it
//   - GOAT_LAB_TARGET_IP          mesh IP of a probe peer reachable through
//                                 the wg-cp0 tunnel (TCP-connect target)
//
// Optional env:
//
//   - GOAT_LAB_TIMEOUT_SEC        handshake-deadline override; default 30
//   - GOAT_LAB_PROBE_TIMEOUT_SEC  TCP-probe timeout; default 5
//   - GOAT_LAB_PROBE_PORT         TCP probe port on target; default 80
//
// Failure-signal discrimination: each error path is tagged
// `[phase=<import|connect|handshake|probe>]` so the workflow can surface
// which leg of the smoke broke. On a first failure we Disconnect + sleep
// 60s + retry once — gives the daemon a chance to cycle through additional
// KnownEndpoints if the first selection was unreachable.
func TestRealProtocol_BundleImportAndHandshake(t *testing.T) {
	bundlePath := os.Getenv("GOAT_LAB_BUNDLE_PATH")
	trustPath := os.Getenv("GOAT_LAB_TRUST_ROOTS_PATH")
	targetIP := os.Getenv("GOAT_LAB_TARGET_IP")
	if bundlePath == "" || trustPath == "" || targetIP == "" {
		t.Skip("GOAT_LAB_BUNDLE_PATH / GOAT_LAB_TRUST_ROOTS_PATH / GOAT_LAB_TARGET_IP not set; sandbox lab smoke skipped")
	}

	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("[phase=setup] read lab bundle: %v", err)
	}
	pemBytes, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("[phase=setup] read lab trust roots: %v", err)
	}

	timeoutSec := envInt("GOAT_LAB_TIMEOUT_SEC", 30)
	probeTimeoutSec := envInt("GOAT_LAB_PROBE_TIMEOUT_SEC", 5)
	probePort := envStr("GOAT_LAB_PROBE_PORT", "80")

	dir := shortTempDir(t)
	labTrustPath := filepath.Join(dir, "trust-roots.pem")
	if err := os.WriteFile(labTrustPath, pemBytes, 0o600); err != nil {
		t.Fatalf("[phase=setup] write trust roots: %v", err)
	}
	fix := &fixture{
		BundleBytes:     bundleBytes,
		TrustRootsPEM:   pemBytes,
		TrustRootsPath:  labTrustPath,
		SocketPath:      filepath.Join(dir, "ipc.sock"),
		BundleStatePath: filepath.Join(dir, "bundle.cbor"),
	}

	d := startDaemon(t, fix)

	info, err := d.client.ImportBundle(testCtx(), fix.BundleBytes)
	if err != nil {
		t.Fatalf("[phase=import] %v", err)
	}
	t.Logf("imported bundle: issued_to=%s site=%s expires=%s",
		info.IssuedTo, info.Site, info.NotAfter.Format(time.RFC3339))

	for attempt := 1; attempt <= 2; attempt++ {
		err := connectAndProbe(t, d, targetIP, probePort, timeoutSec, probeTimeoutSec)
		if err == nil {
			_ = d.client.Disconnect(testCtx())
			return
		}
		if attempt == 2 {
			_ = d.client.Disconnect(testCtx())
			t.Fatalf("attempt 2/2 failed: %v", err)
		}
		t.Logf("attempt 1/2 failed (%v); disconnecting + sleeping 60s before retry — gives daemon a chance to cycle KnownEndpoints", err)
		_ = d.client.Disconnect(testCtx())
		time.Sleep(60 * time.Second)
	}
}

func connectAndProbe(t *testing.T, d *daemonProc, targetIP, probePort string, handshakeTimeoutSec, probeTimeoutSec int) error {
	t.Helper()
	if err := d.client.Connect(testCtx()); err != nil {
		return fmt.Errorf("[phase=connect] %w", err)
	}

	deadline := time.Now().Add(time.Duration(handshakeTimeoutSec) * time.Second)
	var lastStatus *ipc.StatusInfo
	for time.Now().Before(deadline) {
		st, err := d.client.GetStatus(testCtx())
		if err != nil {
			return fmt.Errorf("[phase=handshake] getStatus: %w", err)
		}
		lastStatus = st
		if st.State == ipc.StateConnected && !st.LastHandshake.IsZero() {
			t.Logf("handshake confirmed: in=%d out=%d last_handshake=%s",
				st.BytesIn, st.BytesOut, st.LastHandshake.Format(time.RFC3339))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastStatus == nil || lastStatus.State != ipc.StateConnected || lastStatus.LastHandshake.IsZero() {
		return fmt.Errorf("[phase=handshake] no completion within %ds; final_status=%+v", handshakeTimeoutSec, lastStatus)
	}

	probeAddr := net.JoinHostPort(targetIP, probePort)
	conn, err := net.DialTimeout("tcp", probeAddr, time.Duration(probeTimeoutSec)*time.Second)
	if err != nil {
		return fmt.Errorf("[phase=probe] tcp connect %s via wg-cp0: %w", probeAddr, err)
	}
	_ = conn.Close()
	t.Logf("probe ok: tcp connect %s via wg-cp0 succeeded within %ds", probeAddr, probeTimeoutSec)
	return nil
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
