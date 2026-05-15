package innermesh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/innermesh/fakemgmt"
	"github.com/dlf-dds/goat-client/internal/innermesh/fakesignal"
)

// TestNetbird_LifecycleAgainstFakes drives the full Configure → Connect →
// Status → Disconnect lifecycle against in-process fake mgmt + signal
// servers. This is the gate that the M2a/M2b validation gap was meant
// to close: until this test exists, "the embed library is wired up"
// was an unverified claim.
//
// Setup: fakesignal first (so we know its addr), then fakemgmt with
// WithSignalURI plumbing the signal addr into Login responses, then
// innermesh.Netbird configured to point at the fakemgmt address.
//
// Note: embed.New globally sets NB_USE_NETSTACK_MODE=true, so the
// gVisor netstack handles WireGuard in-process — no real OS interface
// is created. That's what makes this test runnable in CI without
// admin/root.
func TestNetbird_LifecycleAgainstFakes(t *testing.T) {
	// Upstream netbird's embed.Client has a known race between its
	// internal connect loop ((*ConnectClient).run.func4 reading engine
	// state) and Stop ((*Engine).close writing engine state). The
	// race surfaces deterministically on darwin/arm64 under -race;
	// passes on linux/{amd64,arm64} + windows/amd64 because of
	// scheduling differences. The race is in vendored upstream netbird,
	// not in our innermesh code — fixing it requires a patch to
	// dlf-dds/netbird's client/embed/embed.go to add proper
	// synchronization around the engine pointer.
	//
	// Skip under -race until the upstream patch lands. Tests still
	// run without -race on every PR-gate matrix leg, so functional
	// regressions still gate merges.
	if raceDetectorEnabled {
		t.Skip("upstream netbird embed.Client connect/stop race; tracked separately")
	}

	sig, err := fakesignal.Listen(t)
	if err != nil {
		t.Fatalf("fakesignal.Listen: %v", err)
	}
	mgmt, err := fakemgmt.Listen(t, fakemgmt.WithSignalURI(sig.Addr()))
	if err != nil {
		t.Fatalf("fakemgmt.Listen: %v", err)
	}

	nb := NewNetbird("goat-test-peer")
	t.Cleanup(func() { _ = nb.Close() })

	cfg := Config{
		// "http://" prefix → embed treats as plaintext gRPC (TLS off).
		ManagementURL: "http://" + mgmt.Addr(),
		// auth.registerPeer's setup-key validation is uuid.Parse; the
		// fake doesn't actually validate, but embed.New requires a
		// non-empty SetupKey/JWT/PrivateKey, and the auth path may
		// uuid.Parse this if it ever falls into the register branch.
		SetupKey: "11111111-1111-1111-1111-111111111111",
	}
	if err := nb.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// 30s is generous — engine startup involves wg interface creation,
	// DNS server, route + firewall managers. Local-loopback RPCs
	// should land in <1s; the budget is for setup work, not network.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := nb.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v\nlogs:\n%s", err, strings.Join(nb.Logs(50), "\n"))
	}

	if got := nb.State(); got != StateUp {
		t.Errorf("State after Connect: got %v, want %v\nlogs:\n%s",
			got, StateUp, strings.Join(nb.Logs(50), "\n"))
	}

	stats, err := nb.Stats()
	if err != nil {
		t.Errorf("Stats: %v", err)
	}
	if stats.PeerCount != 0 {
		t.Errorf("PeerCount: got %d, want 0 (empty mesh)", stats.PeerCount)
	}

	if err := nb.Disconnect(context.Background()); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if got := nb.State(); got != StateClosed {
		t.Errorf("State after Disconnect: got %v, want %v", got, StateClosed)
	}
}
