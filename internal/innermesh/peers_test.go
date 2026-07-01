package innermesh

import (
	"context"
	"testing"
)

func TestPeerStatusPath(t *testing.T) {
	if got := (PeerStatus{Relayed: false}).Path(); got != "direct" {
		t.Fatalf("direct peer Path() = %q, want \"direct\"", got)
	}
	if got := (PeerStatus{Relayed: true}).Path(); got != "relayed" {
		t.Fatalf("relayed peer Path() = %q, want \"relayed\"", got)
	}
}

func TestFakePeersEmptyWhenDown(t *testing.T) {
	f := NewFake()
	// StateClosed on construction: no peers surfaced.
	got, err := f.Peers()
	if err != nil {
		t.Fatalf("Peers() err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Peers() while down returned %d, want 0", len(got))
	}
}

func TestFakePeersWhenUp(t *testing.T) {
	f := NewFake()
	if err := f.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got, err := f.Peers()
	if err != nil {
		t.Fatalf("Peers() err = %v", err)
	}
	if len(got) != f.peerCnt {
		t.Fatalf("Peers() returned %d, want peerCnt %d", len(got), f.peerCnt)
	}
	// The synthetic set includes both a direct and a relayed peer so the
	// badge has both states to render.
	var direct, relayed int
	for _, p := range got {
		if p.IP == "" || p.PubKey == "" {
			t.Fatalf("peer missing identity fields: %+v", p)
		}
		switch p.Path() {
		case "direct":
			direct++
		case "relayed":
			relayed++
		}
	}
	if direct == 0 || relayed == 0 {
		t.Fatalf("want both direct and relayed peers, got direct=%d relayed=%d", direct, relayed)
	}
}

func TestFakePeersReturnsCopy(t *testing.T) {
	f := NewFake()
	if err := f.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got, _ := f.Peers()
	if len(got) == 0 {
		t.Fatal("expected peers")
	}
	got[0].IP = "mutated"
	again, _ := f.Peers()
	if again[0].IP == "mutated" {
		t.Fatal("Peers() leaked its backing slice; a caller mutation changed internal state")
	}
}
