package fakemgmt

import (
	"context"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/netbirdio/netbird/encryption"
	mgmtproto "github.com/netbirdio/netbird/shared/management/proto"
)

// Compile-time assertion: Server implements the gRPC interface.
// Future netbird-proto bumps that reshape the interface fail
// loudly here rather than at runtime.
var _ mgmtproto.ManagementServiceServer = (*Server)(nil)

func TestListenAndServerKey(t *testing.T) {
	t.Parallel()
	srv, err := Listen(t)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if srv.Addr() == "" {
		t.Fatal("Addr empty after Listen")
	}

	// Dial the fake's gRPC port and call GetServerKey.
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	client := mgmtproto.NewManagementServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := client.GetServerKey(ctx, &mgmtproto.Empty{})
	if err != nil {
		t.Fatalf("GetServerKey: %v", err)
	}
	if reply.Key == "" {
		t.Fatal("ServerKey reply empty")
	}
	// Round-trip property: the key in the reply must be the public
	// half of the server's stored private key.
	want := srv.PublicKey().String()
	if reply.Key != want {
		t.Errorf("ServerKey: got %q, want %q", reply.Key, want)
	}
	// And it must parse as a valid WG public key.
	if _, err := wgtypes.ParseKey(reply.Key); err != nil {
		t.Errorf("ServerKey parse: %v (response not a valid WG key)", err)
	}
}

func TestIsHealthy(t *testing.T) {
	t.Parallel()
	srv, err := Listen(t)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	client := mgmtproto.NewManagementServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.IsHealthy(ctx, &mgmtproto.Empty{}); err != nil {
		t.Fatalf("IsHealthy: %v", err)
	}
}

func TestLoginRoundTrip(t *testing.T) {
	// Unit-level check on the encrypted Login envelope: build a
	// LoginRequest, encrypt against the server's pubkey, send, decrypt
	// the response, assert the canned PeerConfig + signal URI come back.
	// Cheaper to debug than the full embed-driven lifecycle test in
	// internal/innermesh/netbird_test.go — if encryption is broken this
	// fails with a small surface.
	t.Parallel()
	wantSignal := "127.0.0.1:65535"
	srv, err := Listen(t, WithSignalURI(wantSignal))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	clientKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}

	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	client := mgmtproto.NewManagementServiceClient(conn)

	body, err := encryption.EncryptMessage(srv.PublicKey(), clientKey, &mgmtproto.LoginRequest{})
	if err != nil {
		t.Fatalf("encrypt LoginRequest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	encResp, err := client.Login(ctx, &mgmtproto.EncryptedMessage{
		WgPubKey: clientKey.PublicKey().String(),
		Body:     body,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	var resp mgmtproto.LoginResponse
	if err := encryption.DecryptMessage(srv.PublicKey(), clientKey, encResp.GetBody(), &resp); err != nil {
		t.Fatalf("decrypt LoginResponse: %v", err)
	}
	if got := resp.GetPeerConfig().GetAddress(); got == "" {
		t.Errorf("PeerConfig.Address: want non-empty, got %q", got)
	}
	if got := resp.GetNetbirdConfig().GetSignal().GetUri(); got != wantSignal {
		t.Errorf("NetbirdConfig.Signal.Uri: got %q, want %q", got, wantSignal)
	}
}
