package innermesh

import (
	"context"
	"testing"
)

func TestFakeLifecycle(t *testing.T) {
	t.Parallel()
	m := NewFake()
	if got := m.State(); got != StateClosed {
		t.Errorf("fresh: state=%v want closed", got)
	}
	if err := m.Configure(Config{SetupKey: "k", ManagementURL: "https://mgmt.example/"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if err := m.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := m.State(); got != StateUp {
		t.Errorf("post-connect: state=%v want up", got)
	}
	st, err := m.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.PeerCount == 0 {
		t.Error("expected non-zero peer count when up")
	}
	if err := m.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if got := m.State(); got != StateClosed {
		t.Errorf("post-disconnect: state=%v want closed", got)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
