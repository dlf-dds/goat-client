// Package filedrop is goat-client's peer-to-peer file transfer ("goatdrop"),
// the Taildrop equivalent (design: goat-client-connectivity-and-filedrop,
// ADR 1063 in the DesertBreadBird core repo).
//
// The model is deliberately the same as Taildrop's: each node runs a small
// HTTP server bound to its own inner-mesh (overlay) IP, exposing
// PUT /put/<name> that streams the body to an inbox directory; a sender
// PUTs a file to a peer's overlay IP:port. Two properties the mesh already
// provides carry the security:
//
//   - Confidentiality: the transfer rides the WireGuard tunnel, which is
//     encrypted. Plain HTTP over the tunnel is the faithful Taildrop
//     mechanism; TLS on top is redundant for v1 (a hardening follow-up).
//   - Authentication: the connection's source overlay IP *is* the
//     authenticated identity — a peer cannot put packets on the tunnel with
//     another peer's source IP without its key. The server resolves the
//     source IP against an Authorizer (the daemon wires this to the live
//     NetBird peer list); an unresolved source is refused. No separate
//     token — that would be redundant with what the mesh enforces.
//
// This package is a pure library: the Server + Send client + the Authorizer
// seam, testable over loopback. Daemon lifecycle, the identity gate against
// the live peer set, the CLI, and the GUI drop zone wire it up elsewhere.
package filedrop

import "time"

// DefaultPort is the TCP port the receive Server binds on the node's
// overlay IP by default. Mesh-only; never exposed on a public interface.
const DefaultPort = 51823

// putPrefix is the URL path prefix for an inbound file: PUT /put/<name>.
const putPrefix = "/put/"

// DefaultMaxBytes caps a single inbound file. A sender exceeding it is
// refused before the body is written. 0 on the Server falls back to this.
const DefaultMaxBytes = 2 << 30 // 2 GiB

// Received describes a completed inbound transfer, handed to the Server's
// OnReceive hook (the daemon turns these into a notification + the
// incoming-files list the GUI shows).
type Received struct {
	Name   string    // sanitized base name as stored
	Size   int64     // bytes written
	From   string    // peer label from the Authorizer ("" if unknown-but-allowed)
	FromIP string    // source overlay IP
	Path   string    // absolute path where it landed
	At     time.Time // completion time
}

// Authorizer decides whether a connection from srcIP (a peer's overlay IP)
// may drop a file, and returns a human label for that peer. The daemon
// wires this to the live NetBird peer list. A nil Authorizer on the Server
// means fail-closed: every drop is refused.
type Authorizer interface {
	// Authorize reports whether srcIP is an allowed peer, and a label for
	// it (name/FQDN) for the Received record. ok=false rejects the drop.
	Authorize(srcIP string) (label string, ok bool)
}

// AuthorizerFunc adapts a function to the Authorizer interface.
type AuthorizerFunc func(srcIP string) (string, bool)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(srcIP string) (string, bool) { return f(srcIP) }
