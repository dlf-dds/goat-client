// Package integration drives the goat-clientd binary end-to-end through
// its IPC surface (the Client interface in internal/ipc/types.go).
//
// Three build tags gate three tiers:
//
//   - `integration` (default in CI): builds the daemon, generates a
//     fresh ECDSA P-256 trust-root + a signed CBOR EnrollmentBundle in
//     the test process, spawns the daemon on a temp Unix socket, dials
//     it via ipc.NewClient("unix://..."), and exercises ImportBundle /
//     GetStatus / GetDiagnostics + the no-bundle Connect-rejection
//     path. Hermetic, no network, no privileged ops. Connect post-
//     import is NOT exercised at this tier — Track A's daemon brings
//     up real wg-cp0 via wireguard-go which requires CAP_NET_ADMIN /
//     a real TUN device; that coverage lives in the realprotocol
//     sibling.
//
//   - `realprotocol` (sibling, nightly): same harness, real CBOR
//     bundle from the goat sandbox lab, brings the wg-cp0 outer tunnel
//     up against a live endpoint. Skips unless GOAT_LAB_BUNDLE_PATH +
//     GOAT_LAB_TRUST_ROOTS_PATH are set.
//
//   - `mobile_realprotocol` (in-process mobile engine): the regression
//     test for the gomobile facade's tunnel wireup. Two in-process
//     wireguard-go devices on netstack tun.Devices act as the client
//     (driven through internal/tunnel.runMobileEngine — the same
//     engine the iOS NEPacketTunnelProvider and Android VpnService
//     shells call) and the wg-cp0 relay endpoint. The test mints a
//     fresh ECDSA P-256 trust root, signs a bundle in-process, runs
//     it through the real import pipeline (bundle.Unmarshal +
//     trustanchor.Set.Verify + EnrollmentBundle.Verify) +
//     tunnel.FromBundle, brings up runMobileEngine over the netstack
//     tun, and asserts the WG handshake completes by pinging the
//     endpoint's mesh address. No lab + no privileged ops; always
//     runs on every PR.
//
// Sibling pattern lifted from goat-trunk's Block 50G/I real-protocol
// e2e harness (docs/design/real-protocol-e2e-validation.md).
package integration
