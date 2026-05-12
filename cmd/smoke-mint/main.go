// Tiny smoke bundle minter. Builds an EnrollmentBundle, signs with the
// test ECDSA CA, writes to /tmp/smoke-bundle-<target>.cbor. Two targets:
// "android" (endpoint = 10.0.2.2:51820 — emulator-to-host loopback) and
// "ios" (endpoint = 127.0.0.1:51820 — simulator-on-host).
package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
)

func main() {
	caKeyPath := flag.String("ca-key", "/tmp/smoke-ca/test-ca.key.pem", "")
	endpointPubB64 := flag.String("endpoint-pub", "", "wg endpoint pubkey (base64)")
	clientPrivB64 := flag.String("client-priv", "", "wg client privkey (base64)")
	clientPubB64 := flag.String("client-pub", "", "wg client pubkey (base64)")
	endpointAddr := flag.String("endpoint-addr", "", "wg endpoint host:port (e.g. 10.0.2.2:51820)")
	out := flag.String("out", "", "output .cbor path")
	flag.Parse()
	if *out == "" || *endpointAddr == "" || *endpointPubB64 == "" || *clientPrivB64 == "" || *clientPubB64 == "" {
		log.Fatal("required: --out --endpoint-addr --endpoint-pub --client-priv --client-pub")
	}

	keyPEM, err := os.ReadFile(*caKeyPath)
	must(err, "read ca key")
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		log.Fatal("ca key not PEM")
	}
	caKey, err := x509.ParseECPrivateKey(block.Bytes)
	must(err, "parse ca key")

	endpointPub, err := base64.StdEncoding.DecodeString(*endpointPubB64)
	must(err, "decode endpoint pub")
	clientPriv, err := base64.StdEncoding.DecodeString(*clientPrivB64)
	must(err, "decode client priv")
	clientPub, err := base64.StdEncoding.DecodeString(*clientPubB64)
	must(err, "decode client pub")

	now := time.Now().UTC()
	b := bundle.EnrollmentBundle{
		Version:            1,
		DeviceID:           "smoke-mobile-01",
		Site:               "smoke-lab",
		ACLGroups:          []string{"smoke"},
		IssuedAt:           now,
		ActivationDeadline: now.Add(7 * 24 * time.Hour),
		ExpiresAt:          now.Add(30 * 24 * time.Hour),
		Nonce:              randBytes(16),
		CAID:               "smoke-test-ca-ecdsa",
		PeerPubkey:         clientPub, // placeholder; mesh-side
		CPDevicePubkey:     clientPub,
		CPDevicePrivkey:    clientPriv,
		CPDeviceAddress:    "198.18.0.100/24",
		KnownEndpoints: []bundle.KnownEndpoint{
			{
				Addr:     *endpointAddr,
				Pubkey:   endpointPub,
				Kind:     bundle.KindRelay,
				MeshAddr: "198.18.0.2",
			},
		},
	}

	signable, err := b.Signable()
	must(err, "signable")
	digest := sha256.Sum256(signable)
	sig, err := ecdsa.SignASN1(rand.Reader, caKey, digest[:])
	must(err, "sign")
	b.Signature = sig

	full, err := b.Marshal()
	must(err, "marshal")
	must(os.WriteFile(*out, full, 0o600), "write")
	//nolint:forbidigo // operator-facing smoke tool; stdout is the interface
	fmt.Printf("wrote %s (%d bytes, endpoint=%s)\n", *out, len(full), *endpointAddr)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func must(err error, ctx string) {
	if err != nil {
		log.Fatalf("%s: %v", ctx, err)
	}
}
