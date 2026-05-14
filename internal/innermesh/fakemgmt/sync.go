package fakemgmt

import (
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/netbirdio/netbird/encryption"
	mgmtproto "github.com/netbirdio/netbird/shared/management/proto"
)

// Sync implements the encrypted server-streaming
// ManagementService.Sync RPC. The fake sends exactly one
// SyncResponse — an empty-mesh NetworkMap (this peer's address, no
// remote peers, no routes, no DNS) — then blocks on the stream
// context until the embed client cancels or disconnects.
//
// "Empty mesh" is enough to drive innermesh.Netbird through Connect →
// StateUp → Disconnect: the engine receives the initial map, applies
// it (no peers to wire up, no routes to install), and reports
// PeerCount=0 via Status. Multi-peer scenarios are M3+ territory and
// will likely run against a real netbird-mgmt container instead.
func (s *Server) Sync(msg *mgmtproto.EncryptedMessage, srv mgmtproto.ManagementService_SyncServer) error {
	clientPubKey, err := wgtypes.ParseKey(msg.GetWgPubKey())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "fakemgmt: parse client wgPubKey: %v", err)
	}

	var req mgmtproto.SyncRequest
	if err := encryption.DecryptMessage(clientPubKey, s.wgKey, msg.GetBody(), &req); err != nil {
		return status.Errorf(codes.InvalidArgument, "fakemgmt: decrypt SyncRequest: %v", err)
	}

	resp := &mgmtproto.SyncResponse{
		NetbirdConfig: s.netbirdConfig(),
		// Mirror PeerConfig at top-level too: SyncResponse.peerConfig is
		// deprecated in favor of NetworkMap.peerConfig, but the embed
		// client still reads both during transition. Set both to the
		// same canned value.
		PeerConfig: s.peerConfig(),
		NetworkMap: &mgmtproto.NetworkMap{
			Serial:               1,
			PeerConfig:           s.peerConfig(),
			RemotePeersIsEmpty:   true,
			FirewallRulesIsEmpty: true,
		},
	}

	body, err := encryption.EncryptMessage(clientPubKey, s.wgKey, resp)
	if err != nil {
		return status.Errorf(codes.Internal, "fakemgmt: encrypt SyncResponse: %v", err)
	}

	if err := srv.Send(&mgmtproto.EncryptedMessage{
		WgPubKey: s.wgKey.PublicKey().String(),
		Body:     body,
	}); err != nil {
		return err
	}

	// Hold the stream open until the client cancels. The empty-mesh
	// fake never pushes follow-up updates.
	<-srv.Context().Done()
	return nil
}
