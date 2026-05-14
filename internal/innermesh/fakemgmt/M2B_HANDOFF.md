# M2b handoff — encrypted Login + streaming Sync

> **Status**: M2a landed (this branch, commit `ed31c55`). The
> `Server` type implements `proto.ManagementServiceServer` with
> `GetServerKey` + `IsHealthy` real and everything else returning
> `Unimplemented` via the embedded `UnimplementedManagementServiceServer`.
> M2b implements `Login` + `Sync` so an `innermesh.Netbird` Connect
> can actually drive a successful initial handshake against this
> fake, closing the "validation status" gap UNSTRIP.md flagged.
>
> **For the session picking this up**: read this whole file, then
> read `server.go` + `server_test.go`. The skeleton is done; M2b
> fills in the protocol bodies.

## Where you are

- Branch: `track/v0.2-netbird-fakemgmt-76N-M2`
- Worktree:
  `.claude/worktrees/v0.2-netbird-fakemgmt-76N-M2/` (still
  provisioned; resume in place or
  `git fetch origin && git rebase origin/main` if main moved)
- Last commit on branch: `ed31c55` (M2a skeleton)
- Not yet pushed to a PR — open as draft when M2b's first compile
  succeeds.

## What the embed client needs from us

`embed.Client.Start(ctx)` calls these RPCs in order during the
initial bring-up (read `client/embed/embed.go` for the canonical
flow; the helper is `internal.NewClient` plus
`client.Run(run, "")`):

1. **`GetServerKey(Empty) → ServerKeyResponse`** — done in M2a.
2. **`Login(EncryptedMessage) → EncryptedMessage`** — TODO.
   - The client encrypts a `LoginRequest` protobuf message using
     Curve25519 ECDH + ChaCha20Poly1305 against the server's WG
     pubkey (from step 1).
   - Server decrypts via `encryption.DecryptMessage(remotePubKey,
     ourPrivateKey, encryptedBytes, &target)` — both helpers are in
     `github.com/netbirdio/netbird/encryption`, both take
     `wgtypes.Key` for the WG key types.
   - Server inspects the `LoginRequest` (setup key, peer pubkey,
     wireguard-pubkey field, system info). Any setup key accepted
     in the fake — no real auth.
   - Server constructs a `LoginResponse` with a `WiretrusteeConfig`
     containing stun/turn URLs (canned) + signal URL (point at a
     fake signal server if we ship one, else empty string with
     graceful fallback in embed — verify which).
   - Server encrypts the response via
     `encryption.EncryptMessage(remotePubKey, ourPrivateKey,
     responseMessage)` and returns as `EncryptedMessage`.
3. **`Sync(EncryptedMessage, ManagementService_SyncServer) error`** — TODO.
   - Server-streaming. Receives one `SyncRequest` (encrypted), then
     keeps the stream open and sends `SyncResponse` messages
     whenever peer-list / route / DNS config changes.
   - For the M2b "empty mesh" case: send one initial `SyncResponse`
     with `NetworkMap` populated as: this device's network address
     (canned, e.g., 10.42.0.7/16), empty peer list, empty route list,
     empty DNS config. The peer-pubkey field on the device's own
     `RemotePeerConfig` is its own WG pubkey from the Login request.
   - Keep the stream alive (block on `ctx.Done()` of the stream
     context) until the client cancels or disconnects.
4. **`IsHealthy(Empty) → Empty`** — done in M2a.

## Encryption envelope walkthrough

netbird's mgmt-API encryption uses Curve25519 ECDH for the shared
secret, then ChaCha20Poly1305 to seal the message. The
`github.com/netbirdio/netbird/encryption` package's
`message.go` exposes:

```go
func EncryptMessage(remotePubKey wgtypes.Key, ourPrivateKey wgtypes.Key, message pb.Message) ([]byte, error)
func DecryptMessage(remotePubKey wgtypes.Key, ourPrivateKey wgtypes.Key, encryptedMessage []byte, message pb.Message) error
```

`message` is any proto.Message — pass a fresh `&proto.LoginRequest{}`
to Decrypt (the helper fills it in-place); pass the constructed
response to Encrypt.

For Login: the `EncryptedMessage` wire form carries a `WgPubKey`
field (32 bytes) — that's the client's WG pubkey, which is the
remote-pubkey side of the ECDH on our end. Server uses
`EncryptedMessage.WgPubKey` to identify the client + as the remote-
pubkey arg to Decrypt/Encrypt.

## Sync stream lifecycle

The Sync RPC method signature (from `management_grpc.pb.go`):

```go
type ManagementService_SyncServer interface {
    Send(*EncryptedMessage) error
    grpc.ServerStream
}
```

So `Sync(req *EncryptedMessage, srv ManagementService_SyncServer)
error`. Pattern:

```go
func (s *Server) Sync(req *mgmtproto.EncryptedMessage, srv mgmtproto.ManagementService_SyncServer) error {
    // Decrypt SyncRequest to identify the peer + its session keys.
    var syncReq mgmtproto.SyncRequest
    if err := encryption.DecryptMessage(...); err != nil { ... }

    // Build the initial SyncResponse (empty mesh).
    resp := &mgmtproto.SyncResponse{ NetworkMap: ... }

    // Encrypt + send the initial response.
    encryptedBytes, err := encryption.EncryptMessage(remotePub, s.wgKey, resp)
    if err != nil { return err }
    if err := srv.Send(&mgmtproto.EncryptedMessage{
        WgPubKey: s.wgKey.PublicKey().String(),
        Body: encryptedBytes,
    }); err != nil { return err }

    // Block until the stream context is canceled (client disconnect
    // or test tear-down). The fake doesn't push updates — empty mesh
    // stays empty.
    <-srv.Context().Done()
    return nil
}
```

## Open question: does Sync's response force a fake signal server?

netbird's embed dials the signal server using the URL from Login's
`WiretrusteeConfig.SignalConfig.Uri`. If we set that to an empty
string, embed may either (a) gracefully treat "no signal = peer
discovery via mgmt only" or (b) fail-fast. Verify by:

1. Send Login response with `WiretrusteeConfig.SignalConfig =
   &HostConfig{Uri: ""}`.
2. Watch the embed client's logs (LogOutput on
   `innermesh.Netbird`'s ring buffer).
3. If embed errors out on empty signal URL: we need a fake signal
   server too. Spin one up alongside the mgmt fake using
   `signal/proto/signalexchange_grpc.pb.go`'s
   `SignalExchangeServer` interface. Pattern identical to this
   package — embed `UnimplementedSignalExchangeServer`, implement
   the bare minimum.

If (a) — no signal needed — M2b is just mgmt. If (b) — we ship
`internal/innermesh/fakesignal/` too. Probably the test that drives
the embed client will tell us within minutes.

## Acceptance criteria for M2b

- [ ] `Login` decrypts a real LoginRequest, returns a valid
      LoginResponse.
- [ ] `Sync` accepts the initial SyncRequest, sends one
      SyncResponse, keeps the stream open until ctx-cancel.
- [ ] New `netbird_test.go` in `internal/innermesh/` (NOT in
      fakemgmt) that:
      1. Calls `fakemgmt.Listen(t)`.
      2. Constructs `innermesh.NewNetbird("test-device")`.
      3. Configures with `ManagementURL = "http://" +
         srv.Addr()` + a setup key.
      4. Connects — passes if no error within 5s ctx.
      5. Reads Status — passes if `State == StateUp` and
         `PeerCount == 0`.
      6. Disconnects — passes if no error.
- [ ] `make smoke-modes` `netbird-only` variant runs against
      this fake (M4 territory; M2b just enables it).
- [ ] No tests in M2a get marked as expecting Unimplemented for
      Login any more (the
      `TestLoginReturnsUnimplemented` test should be removed or
      flipped to assert a real response).
- [ ] All six desktop targets cross-compile clean with the new
      crypto/protobuf code.

## Files M2b will likely touch

- `internal/innermesh/fakemgmt/login.go` (new) — Login impl.
- `internal/innermesh/fakemgmt/sync.go` (new) — Sync impl + stream
  lifecycle.
- `internal/innermesh/fakemgmt/server.go` — small additions
  (helpers for ECDH state).
- `internal/innermesh/fakemgmt/server_test.go` — remove
  `TestLoginReturnsUnimplemented`; the new Login test in
  `internal/innermesh/netbird_test.go` covers the real path.
- `internal/innermesh/netbird_test.go` (new) — drive
  `innermesh.Netbird` against the fake.
- Possibly `internal/innermesh/fakesignal/` (new) if Login's
  empty-signal-URL doesn't gracefully fall through.

## Pivot question worth raising before M2b

Operator flagged this in the M2a session: M2b is heavy (300-600 LOC
of careful protocol code). Alternative paths to consider before
investing:

1. **Land M2b as proposed** — full in-process fake mgmt-server with
   real encryption. Hermetic, fast, CI-friendly. Hours of focused
   work, real risk of protocol bugs.
2. **Pivot to container-in-CI** — run real `netbird` + `netbird-mgmt`
   in docker-compose inside the integration test workflow. Honest
   about what's tested (real protocol at real wire depth). Tests
   take longer + need Docker; bigger CI cache. No in-process fake
   needed.
3. **Hybrid** — M2b ships a partial fake good enough for unit-level
   `Netbird` lifecycle tests (Login + empty-mesh Sync), and a
   real-container integration test handles the multi-peer / signal
   /relay path. This is probably the right end state regardless.

Recommend raising this with the operator at session start before
investing in (1).

## Resume checklist

```bash
# At session start:
cd /Users/dene/src/github.com/dlf-dds/goat-client/
git fetch origin main
# Either resume the existing worktree:
cd .claude/worktrees/v0.2-netbird-fakemgmt-76N-M2
# Or, if main has moved meaningfully, provision fresh:
#   /iso enter v0.2-netbird-fakemgmt-76N-M2-resume

git log --oneline -3  # confirm at ed31c55 (M2a)
go test ./internal/innermesh/fakemgmt/  # confirm M2a still green

# Then read this file, server.go, server_test.go, and dive into M2b.
```
