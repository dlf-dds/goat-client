package fakemgmt

import (
	"context"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

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

func TestLoginReturnsUnimplemented(t *testing.T) {
	// M2a only ships GetServerKey + IsHealthy real. Login + Sync
	// land in M2b on this same branch. This test pins the M2a
	// behavior — if a future commit silently flips Login to a real
	// impl without updating the test, the test fails (good — that's
	// the prompt to extend coverage).
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
	_, err = client.Login(ctx, &mgmtproto.EncryptedMessage{})
	if err == nil {
		t.Fatal("Login: want Unimplemented error, got nil — M2b landed without updating this test")
	}
}
