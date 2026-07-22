package innermesh

import "testing"

// TestBareOverlayIP — netbird's FullStatus reports the local peer's
// overlay address WITH its CIDR mask (LocalPeerState.IP =
// "100.64.254.108/16"), unlike per-peer IPs which are bare. LocalIP
// must return a bare host IP so the peerping + filedrop listeners can
// bind it. Regression guard for the EFDI v0.3.4 finding:
//
//	filedrop serve: listen tcp: lookup 100.64.254.108/16: no such host
//	peerping start: listen udp: lookup 100.64.254.108/16: no such host
func TestBareOverlayIP(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cidr_v4", "100.64.254.108/16", "100.64.254.108"},
		{"bare_v4", "100.64.254.108", "100.64.254.108"},
		{"cidr_v6", "fd00::1/64", "fd00::1"},
		{"empty", "", ""},
		{"not_an_ip", "garbage", "garbage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bareOverlayIP(tc.in); got != tc.want {
				t.Errorf("bareOverlayIP(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
