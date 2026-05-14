package fakemgmt

import (
	"context"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/netbirdio/netbird/encryption"
	mgmtproto "github.com/netbirdio/netbird/shared/management/proto"
)

// Canned PeerConfig values returned to every client. The fake doesn't
// model peer-IP allocation — every Connect gets the same address. Good
// enough for the empty-mesh lifecycle test (one peer in the network);
// will need per-peer allocation if/when M3 grows multi-peer scenarios.
const (
	fakePeerAddress = "10.42.0.7/16"
	fakePeerFQDN    = "fake-peer.fakemgmt.local"
)

// Login implements the encrypted ManagementService.Login RPC. Accepts
// any LoginRequest (no setup-key validation — this is a fake) and
// returns a LoginResponse pointing the embed client at the
// preconfigured signal URI plus a canned PeerConfig.
//
// Encryption envelope:
//   - msg.WgPubKey is the client's WG pubkey (used as the remote-pubkey
//     side of the ECDH).
//   - msg.Body is the LoginRequest sealed via Curve25519 + ChaCha20Poly1305
//     against the server's pubkey we returned in GetServerKey.
//   - Response is encrypted symmetrically with the same key pair.
func (s *Server) Login(_ context.Context, msg *mgmtproto.EncryptedMessage) (*mgmtproto.EncryptedMessage, error) {
	clientPubKey, err := wgtypes.ParseKey(msg.GetWgPubKey())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "fakemgmt: parse client wgPubKey: %v", err)
	}

	var req mgmtproto.LoginRequest
	if err := encryption.DecryptMessage(clientPubKey, s.wgKey, msg.GetBody(), &req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "fakemgmt: decrypt LoginRequest: %v", err)
	}

	resp := &mgmtproto.LoginResponse{
		PeerConfig:    s.peerConfig(),
		NetbirdConfig: s.netbirdConfig(),
	}

	body, err := encryption.EncryptMessage(clientPubKey, s.wgKey, resp)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "fakemgmt: encrypt LoginResponse: %v", err)
	}

	return &mgmtproto.EncryptedMessage{
		WgPubKey: s.wgKey.PublicKey().String(),
		Body:     body,
	}, nil
}

// peerConfig returns the canned PeerConfig handed to every client.
// Shared between Login + Sync so both responses agree on the peer's
// identity (engine compares them across the two RPCs).
func (s *Server) peerConfig() *mgmtproto.PeerConfig {
	return &mgmtproto.PeerConfig{
		Address: fakePeerAddress,
		Fqdn:    fakePeerFQDN,
	}
}

// netbirdConfig returns the global Netbird config handed to every
// client. The signal URI is plumbed through so embed's connectToSignal
// can dial the fakesignal listener; STUN/TURN/Relay are intentionally
// empty — the empty-mesh lifecycle test never needs ICE candidates.
func (s *Server) netbirdConfig() *mgmtproto.NetbirdConfig {
	return &mgmtproto.NetbirdConfig{
		Signal: &mgmtproto.HostConfig{
			Uri:      s.signalURI,
			Protocol: mgmtproto.HostConfig_HTTP,
		},
	}
}
