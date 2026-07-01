// Package peerping measures live round-trip latency to inner-mesh peers
// over the tunnel and keeps a rolling per-peer history for a latency
// graph. It is the client-side latency source behind goat-client's
// connectivity-check panel (design: goat-client-connectivity-and-filedrop,
// ADR 1061 in the DesertBreadBird core repo).
//
// It is the peer-to-peer analog of internal/reachability, but a
// deliberately different contract: reachability probes wg-cp0 relay
// endpoints for dial-selection and filters RTT jitter out; peerping
// samples a single peer continuously and KEEPS the jitter, because the
// jitter is exactly the signal a live graph renders.
//
// Why an app-layer echo. NetBird's per-peer latency is a one-shot frozen
// at ICE candidate-pair selection and is absent entirely on the relay
// path, so it cannot drive a live graph. peerping runs its own tiny UDP
// echo: a Responder that echoes probe datagrams and a Pinger that times
// the round trip. Both mesh peers run goat-client, so both run the
// Responder. This measures the true overlay-IP RTT regardless of whether
// the underlying path is direct or relayed; the direct-vs-relayed label
// is a separate read from NetBird status (internal/innermesh), not
// measured here.
//
// Wire format is intentionally trivial: a 4-byte magic, a 1-byte
// version, and an 8-byte big-endian sequence number (13 bytes total).
// The Responder echoes the packet verbatim; the Pinger matches the
// sequence number and times the round trip with a monotonic clock. No
// timestamps ride the wire — RTT is measured locally, so peer clock skew
// never distorts it.
package peerping

import (
	"encoding/binary"
	"errors"
	"time"
)

// DefaultPort is the UDP port the Responder binds on the peer's tunnel
// IP by default. Mesh-only; never exposed on a public interface.
const DefaultPort = 51822

// Tunables. Callers override the corresponding fields on Pinger.
const (
	// DefaultInterval is the gap between successive probes in a Run loop.
	DefaultInterval = 1 * time.Second
	// DefaultTimeout is how long a single probe waits for its echo before
	// it is recorded as lost.
	DefaultTimeout = 2 * time.Second
	// DefaultHistory is the number of samples the rolling Ring keeps —
	// enough for a readable graph without unbounded growth.
	DefaultHistory = 120
)

// Wire framing.
const (
	// magic tags a datagram as a goat peer-ping probe so the Responder
	// never echoes unrelated traffic that happens to hit the port.
	magic = "gpng"
	// version guards the framing; bumped only on a wire-incompatible
	// change. The Responder rejects mismatches rather than echoing them.
	version = 1
	// packetLen is len(magic) + 1 version byte + 8 sequence bytes.
	packetLen = len(magic) + 1 + 8
)

// errShortPacket / errBadMagic / errBadVersion classify a datagram that
// is not a well-formed current-version probe. All three mean "ignore
// this datagram," not "fail the socket."
var (
	errShortPacket = errors.New("peerping: datagram shorter than a probe packet")
	errBadMagic    = errors.New("peerping: datagram is not a peer-ping probe")
	errBadVersion  = errors.New("peerping: unsupported probe version")
)

// encodeProbe writes a probe packet for the given sequence number into a
// fresh buffer.
func encodeProbe(seq uint64) []byte {
	b := make([]byte, packetLen)
	copy(b[:4], magic)
	b[4] = version
	binary.BigEndian.PutUint64(b[5:], seq)
	return b
}

// decodeProbe validates a datagram as a current-version probe and
// returns its sequence number. A non-nil error means the datagram is not
// a probe we recognize and should be ignored.
func decodeProbe(b []byte) (seq uint64, err error) {
	if len(b) < packetLen {
		return 0, errShortPacket
	}
	if string(b[:4]) != magic {
		return 0, errBadMagic
	}
	if b[4] != version {
		return 0, errBadVersion
	}
	return binary.BigEndian.Uint64(b[5:packetLen]), nil
}
