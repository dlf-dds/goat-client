package ui

import (
	"testing"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

func TestStateLabel(t *testing.T) {
	cases := []struct {
		in   ipc.State
		want string
	}{
		{ipc.StateDisconnected, "Disconnected"},
		{ipc.StateConnecting, "Connecting..."},
		{ipc.StateConnected, "Connected"},
		{ipc.StateError, "Error"},
		{ipc.State("garbage"), "Disconnected"},
		{ipc.State(""), "Disconnected"},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := stateLabel(tc.in); got != tc.want {
				t.Errorf("stateLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
