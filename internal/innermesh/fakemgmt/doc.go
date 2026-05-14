// Package fakemgmt is an in-process fake netbird management server
// used by `internal/innermesh` tests + the headless `netbird-only`
// smoke (Block 76N M2-M4). It implements the
// `shared/management/proto.ManagementServiceServer` gRPC interface,
// embeds `UnimplementedManagementServiceServer` for forward
// compatibility, and ships canned-but-protocol-valid responses for
// whichever subset of the netbird mgmt RPCs the embed client
// exercises during startup + steady-state.
//
// # Why an in-process fake (not a real netbird mgmt-server)
//
// Tests need to exercise `innermesh.Netbird.Configure` →
// `Connect` → `Status` → `Disconnect` without standing up a real
// netbird mgmt + signal + relay container stack. The brief
// (Block 76N deliverable #7) calls for an in-process fake so the
// smoke is hermetic, fast, and CI-friendly.
//
// # Scope (incremental — split across two commits on this branch)
//
// **M2a (this commit, the package skeleton)**:
//
//   - Server type with gRPC lifecycle (bind on random port, serve,
//     graceful stop).
//   - `GetServerKey(Empty) → ServerKeyResponse` — returns the
//     fake's WG public key. Required by every embed startup so the
//     client can build the encryption envelope for Login/Sync.
//   - `IsHealthy(Empty) → Empty` — trivial health-check; embed
//     polls this before Login on some code paths.
//   - `Listen(t *testing.T) (*Server, error)` test helper: bind on
//     127.0.0.1:0, register the server, return a handle with URL
//     + Stop func. t.Cleanup wires the Stop.
//   - Compile-time assertion `var _ proto.ManagementServiceServer
//     = (*Server)(nil)` so future netbird-proto bumps don't
//     silently regress the implementation surface.
//
// **M2b (next commit, lands on this branch)**:
//
//   - `Login(EncryptedMessage) → EncryptedMessage` — decrypts the
//     client's login request via `encryption.DecryptMessage`,
//     accepts any setup key (no real auth), returns an encrypted
//     LoginResponse with a canned `TURN`/`STUN` credential set.
//   - `Sync(EncryptedMessage, stream) → stream EncryptedMessage` —
//     server-streaming RPC. Returns one initial SyncResponse with
//     an empty peer list + minimal signal config (TBD: whether we
//     also need a fake signal server), then keeps the stream open
//     until the client cancels.
//   - End-to-end test driving Configure → Connect → Status →
//     Disconnect on `innermesh.Netbird` against the fake.
//
// # Out of scope (for this M2; deferred to M3+)
//
// - Real peer-list updates over Sync (we only need empty + steady;
//   peer add/remove is a future smoke).
// - Fake signal server (added later if Sync's response forces
//   embed to dial signal).
// - Fake relay (handshake never reaches the relay layer with empty
//   peer list).
// - Authentication realism (any setup key is accepted; no token
//   roundtrip).
package fakemgmt
