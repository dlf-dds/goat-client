//go:build mobile_realprotocol

package integration

// TestMobile_RealProtocolHandshake is the in-process real-protocol
// regression test for goat-client's mobile tunnel wireup. It exercises
// the exact code path the Track C (iOS) and Track D (Android) shells
// run — internal/tunnel.runMobileEngine + buildUAPI + wireguard-go
// userspace device — against a second wireguard-go device acting as
// the wg-cp0 relay peer, both rooted on netstack-backed tun.Devices so
// no real OS tun driver is involved. The test mints a fresh ECDSA P-256
// trust-root, builds + signs a CBOR EnrollmentBundle in-process, runs
// the full bundle.Unmarshal + trustanchor.Set.Verify import pipeline
// (proving the verify path), derives a tunnel.Config via
// tunnel.FromBundle, and then drives the mobile engine to completion
// against the in-process peer.
//
// Build tag `mobile_realprotocol` keeps this off the default
// `go test ./...` run. Run with:
//
//   go test -tags=mobile_realprotocol -run TestMobile -v -timeout 60s \
//       ./tests/integration/...
//
// Handshake against an in-process localhost peer should complete in
// well under 5s; the test waits up to 30s before failing.
//
// Why a build tag (not an env var like realprotocol_test.go):
// realprotocol_test.go skips at runtime when its env is unset so it
// can compile-check on every PR. This test always runs to completion
// when its file is included in the build — there's no "skip when no
// lab" branch, and the dependencies (netstack, curve25519, etc.) are
// only worth pulling into the binary when the test is actually
// running. A build tag is the cleaner gate for that shape.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/trustanchor"
	"github.com/dlf-dds/goat-client/internal/tunnel"
)

const (
	mobileTestClientMesh = "198.18.0.100"
	mobileTestPeerMesh   = "198.18.0.2"
	mobileTestMTU        = tunnel.DefaultMTU
)

// wgKeypair is a Curve25519 keypair in raw little-endian 32-byte form
// (the wire form WireGuard UAPI consumes).
type wgKeypair struct {
	priv [32]byte
	pub  [32]byte
}

// genWGKeypair derives a curve25519 keypair the same way `wg genkey` /
// `wg pubkey` does: 32 random bytes clamped per RFC 7748, then
// ScalarBaseMult to get the public key.
func genWGKeypair(t *testing.T) wgKeypair {
	t.Helper()
	var k wgKeypair
	if _, err := rand.Read(k.priv[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	k.priv[0] &= 248
	k.priv[31] &= 127
	k.priv[31] |= 64
	pub, err := curve25519.X25519(k.priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive curve25519 pubkey: %v", err)
	}
	copy(k.pub[:], pub)
	return k
}

// TestMobile_RealProtocolHandshake brings the mobile-tunnel engine up
// over a netstack tun against an in-process wireguard-go peer and
// asserts that a real WG handshake completes — i.e. that the code path
// the gomobile shells call into is wired correctly end-to-end against
// the WG protocol, not just unit-mocked.
//
// Test flow:
//
//  1. mint fresh ECDSA P-256 trust root + trustanchor.Set
//  2. mint two WG curve25519 keypairs (client + endpoint)
//  3. assemble + sign an EnrollmentBundle with the client keypair as
//     CPDevice* and a single KnownEndpoint of Kind=cp-relay pointing
//     at a localhost UDP port the endpoint will listen on
//  4. round-trip Marshal -> Unmarshal -> Set.Verify -> Bundle.Verify
//     to exercise the import pipeline
//  5. derive tunnel.Config via tunnel.FromBundle
//  6. bring up the in-process endpoint peer (netstack tun + wgdevice
//     with endpoint priv key, listening on the chosen port)
//  7. bring up the client engine via tunnel.RunMobileEngineForTest
//     (build-tagged thin wrapper around runMobileEngine) over a
//     second netstack tun
//  8. from the client netstack Net, send an ICMP echo to the endpoint's
//     mesh address — handshake completes inside the WG device as a
//     side-effect of routing the first outbound packet, ping reply
//     comes back through the same path
//  9. assert ping round-trips successfully within 30s
//  10. cancel ctx, assert the engine goroutine returns nil within 10s
func TestMobile_RealProtocolHandshake(t *testing.T) {
	// Step 1: trust root + anchor set.
	rootPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa P-256 trust root: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	anchors, err := trustanchor.NewSet(trustanchor.Anchor{
		Name:       "mobile-realprotocol-test-ca",
		Issuer:     "mobile-realprotocol-test-ca",
		PublicKey:  &rootPriv.PublicKey,
		ValidFrom:  now.Add(-time.Hour),
		ValidUntil: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("construct trustanchor Set: %v", err)
	}

	// Step 2: WG keypairs.
	endpointKP := genWGKeypair(t)
	clientKP := genWGKeypair(t)

	// Step 3a: pick free UDP port for the in-process endpoint.
	endpointPort, err := pickFreeUDPPort()
	if err != nil {
		t.Fatalf("pick free udp port: %v", err)
	}
	endpointAddr := fmt.Sprintf("127.0.0.1:%d", endpointPort)

	// Step 3b: assemble + sign EnrollmentBundle.
	b := &bundle.EnrollmentBundle{
		Version:    bundle.Version,
		DeviceID:   "mobile-realprotocol-test-device",
		PeerPubkey: endpointKP.pub[:],
		ACLGroups:  []string{"mobile-realprotocol-test"},
		Site:       "mobile-realprotocol-lab",
		KnownEndpoints: []bundle.KnownEndpoint{{
			Addr:     endpointAddr,
			Pubkey:   endpointKP.pub[:],
			Kind:     bundle.KindRelay,
			MeshAddr: mobileTestPeerMesh,
		}},
		IssuedAt:           now,
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(24 * time.Hour),
		Nonce:              bytes16("mobile-realproto"),
		CAID:               "mobile-realprotocol-test-ca",
		CPDevicePubkey:     clientKP.pub[:],
		CPDevicePrivkey:    clientKP.priv[:],
		CPDeviceAddress:    mobileTestClientMesh + "/32",
	}
	if err := signBundle(b, rootPriv); err != nil {
		t.Fatalf("sign bundle: %v", err)
	}

	// Step 4: round-trip + verify through the import pipeline.
	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	parsed, err := bundle.Unmarshal(wire)
	if err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	signable, err := parsed.Signable()
	if err != nil {
		t.Fatalf("parsed.Signable: %v", err)
	}
	matchedAnchor, err := anchors.Verify(parsed.Signature, signable)
	if err != nil {
		t.Fatalf("trustanchor verify: %v", err)
	}
	t.Logf("trustanchor verify ok: anchor=%s", matchedAnchor.Name)
	if err := parsed.Verify(&rootPriv.PublicKey); err != nil {
		t.Fatalf("bundle verify (direct): %v", err)
	}

	// Step 5: tunnel.Config from bundle.
	cfg, err := tunnel.FromBundle(parsed)
	if err != nil {
		t.Fatalf("tunnel.FromBundle: %v", err)
	}
	cfg.InterfaceName = "wg-cp0-test"
	cfg.ListenPort = 0
	t.Logf("client tunnel.Config: iface=%s local=%s peer.endpoint=%s peer.allowed=%v",
		cfg.InterfaceName, cfg.LocalAddress, cfg.Peer.Endpoint, cfg.Peer.AllowedIPs)

	// Step 6: in-process endpoint peer.
	endpointTun, _, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(mobileTestPeerMesh)},
		nil,
		mobileTestMTU,
	)
	if err != nil {
		t.Fatalf("create endpoint netstack tun: %v", err)
	}
	endpointLogger := wgdevice.NewLogger(wgdevice.LogLevelError, "(endpoint) ")
	endpointDev := wgdevice.NewDevice(endpointTun, conn.NewDefaultBind(), endpointLogger)
	endpointUAPI := buildEndpointUAPI(endpointKP.priv[:], endpointPort, clientKP.pub[:], mobileTestClientMesh+"/32")
	if err := endpointDev.IpcSet(endpointUAPI); err != nil {
		endpointDev.Close()
		t.Fatalf("endpoint IpcSet: %v", err)
	}
	if err := endpointDev.Up(); err != nil {
		endpointDev.Close()
		t.Fatalf("endpoint dev.Up: %v", err)
	}
	t.Cleanup(func() { endpointDev.Close() })

	// Step 7: client engine over its own netstack tun.
	clientTun, clientNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(mobileTestClientMesh)},
		nil,
		mobileTestMTU,
	)
	if err != nil {
		t.Fatalf("create client netstack tun: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	engineDoneCh := make(chan error, 1)
	go func() {
		engineDoneCh <- tunnel.RunMobileEngineForTest(ctx, clientTun, cfg.InterfaceName, &cfg, nil)
	}()

	// Step 8 + 9: drive a ping from the client netstack to the endpoint's
	// mesh address. The first outbound packet through the WG device
	// triggers a handshake initiation; subsequent retries succeed once
	// the session is established. We use ListenPing/WriteTo +
	// SetReadDeadline so each attempt is bounded and we can loop.
	if err := pingUntilSuccess(t, clientNet, netip.MustParseAddr(mobileTestPeerMesh), 30*time.Second, 500*time.Millisecond); err != nil {
		cancel()
		select {
		case <-engineDoneCh:
		case <-time.After(5 * time.Second):
		}
		t.Fatalf("ping client -> endpoint never succeeded: %v", err)
	}
	t.Logf("ping client -> endpoint succeeded — wg handshake confirmed end-to-end")

	// Step 10: clean shutdown.
	cancel()
	select {
	case err := <-engineDoneCh:
		if err != nil {
			t.Fatalf("runMobileEngine returned error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("runMobileEngine did not shut down within 10s of ctx cancel")
	}
}

// buildEndpointUAPI renders the wireguard-go UAPI text for the in-process
// endpoint peer. The endpoint listens on endpointPort, accepts the client
// pubkey as its single peer, and routes the client's mesh /32 to it.
func buildEndpointUAPI(privKey []byte, endpointPort int, clientPub []byte, clientMesh string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", hex.EncodeToString(privKey))
	fmt.Fprintf(&sb, "listen_port=%d\n", endpointPort)
	sb.WriteString("replace_peers=true\n")
	fmt.Fprintf(&sb, "public_key=%s\n", hex.EncodeToString(clientPub))
	sb.WriteString("replace_allowed_ips=true\n")
	fmt.Fprintf(&sb, "allowed_ip=%s\n", clientMesh)
	return sb.String()
}

// pickFreeUDPPort opens an OS-assigned UDP port on localhost, reads the
// chosen number, and closes the socket — a small TOCTOU window but
// fine for in-process localhost tests (no other binder is racing us).
func pickFreeUDPPort() (int, error) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return 0, err
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port, nil
}

// pingUntilSuccess sends ICMP echo requests from the client netstack
// to dst, retrying every `interval` until either a reply arrives or
// `deadline` elapses. The first send triggers a WG handshake (which
// takes one round-trip ~1ms on localhost); subsequent sends ride the
// established session. Returns nil on success, error on timeout or
// unexpected I/O failure.
func pingUntilSuccess(t *testing.T, n *netstack.Net, dst netip.Addr, total, interval time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(total)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		if err := tryPing(n, dst, interval); err == nil {
			t.Logf("ping attempt %d succeeded", attempt)
			return nil
		} else {
			t.Logf("ping attempt %d: %v", attempt, err)
		}
	}
	return fmt.Errorf("no ping reply after %s (%d attempts)", total, attempt)
}

// tryPing sends one ICMP echo request from n to dst and waits up to
// `timeout` for an echo reply.
func tryPing(n *netstack.Net, dst netip.Addr, timeout time.Duration) error {
	conn, err := n.DialPingAddr(netip.Addr{}, dst)
	if err != nil {
		return fmt.Errorf("dial ping: %w", err)
	}
	defer conn.Close()
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   0xabcd,
			Seq:  1,
			Data: []byte("mobile-realprotocol-test"),
		},
	}
	payload, err := msg.Marshal(nil)
	if err != nil {
		return fmt.Errorf("marshal icmp: %w", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("write icmp: %w", err)
	}
	buf := make([]byte, 1500)
	nRead, _, err := conn.ReadFrom(buf)
	if err != nil {
		return fmt.Errorf("read icmp reply: %w", err)
	}
	reply, err := icmp.ParseMessage(1, buf[:nRead])
	if err != nil {
		return fmt.Errorf("parse icmp reply: %w", err)
	}
	if reply.Type != ipv4.ICMPTypeEchoReply {
		return fmt.Errorf("unexpected icmp reply type %v", reply.Type)
	}
	return nil
}
