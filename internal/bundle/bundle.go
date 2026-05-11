// Package bundle parses and verifies offline-CA-signed enrollment bundles.
//
// The wire schema is replicated from goat-trunk's
// ops/enrollment/bundle/bundle.go (DesertBreadBird@main). Keep the field
// definitions in lock-step: any incompatible change there must bump Version
// here too. The CBOR encoding is canonical (CTAP2: shortest int, sorted
// map keys, definite-length containers) so the same bundle always serialises
// to the same bytes — required for signature verification.
//
// The bundle authorises one device to bring up its wg-cp0 outer tunnel:
// CPDevicePubkey + CPDevicePrivkey + CPDeviceAddress + KnownEndpoints
// give the device-side daemon everything it needs to render a
// single-peer wg-cp0.conf at first boot.
//
// The package also exposes a GUI-side preview (Metadata + Preview) that
// the bundle-import dialog calls on the raw bytes before handing them to
// the daemon. Preview is a real CBOR parse with no signature check —
// the daemon owns the authoritative verify step (TrustRoots.VerifyBundle)
// after the user clicks Apply.
//
// Signature algorithm — goat-trunk Block 79 (commit 3dd4a765, 2026-05-09)
// hard-cut the offline CA root over from Ed25519 to ECDSA P-256 so the
// root cert imports cleanly into macOS / iOS / Android trust stores
// (F-090: macOS Keychain rejects Ed25519 roots with "Unknown format in
// import"). This package mirrors that cutover: Verify accepts an
// *ecdsa.PublicKey on the P-256 curve and uses ecdsa.VerifyASN1 against
// SHA-256 of the canonical CBOR payload. No dual-algorithm support —
// post-Block-79 the Ed25519 root is retired across the substrate.
package bundle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Version is the current bundle format version. Increment on incompatible
// changes; validators reject unknown versions.
const Version uint8 = 1

// Kind identifies the type of a KnownEndpoint.
type Kind string

const (
	KindRelay Kind = "relay"
	KindPeer  Kind = "peer"
	KindMgmt  Kind = "mgmt"
)

// KnownEndpoint is a bootstrap path the device tries in order when
// establishing initial connectivity.
type KnownEndpoint struct {
	Addr   string `cbor:"addr"`
	Pubkey []byte `cbor:"pubkey"`
	Kind   Kind   `cbor:"kind"`

	// MeshAddr is the peer's wg-cp0 mesh-side address (no CIDR; e.g.
	// "198.18.0.2"). Optional, populated only for cp-relay endpoints —
	// the wg-cp0 conf renderer derives AllowedIPs = MeshAddr/32 from
	// this when AllowedIPs (below) is empty.
	MeshAddr string `cbor:"mesh_addr,omitempty"`

	// AllowedIPs, when non-empty, OVERRIDES the default `MeshAddr/32`
	// derivation in the wg-cp0 conf renderer for this endpoint's [Peer]
	// block. Each entry is a CIDR string. Bundle-issuance side populates
	// this from the operator flag --first-relay-route-subnet.
	AllowedIPs []string `cbor:"allowed_ips,omitempty"`
}

// EnrollmentBundle is the signed capability. All fields except Signature
// participate in the signed payload; Signature is produced by the offline
// CA (ECDSA P-256) as an ASN.1-encoded ECDSA signature over SHA-256 of
// the canonical CBOR encoding of the payload fields.
type EnrollmentBundle struct {
	Version            uint8           `cbor:"version"`
	DeviceID           string          `cbor:"device_id"`
	PeerPubkey         []byte          `cbor:"peer_pubkey"`
	ACLGroups          []string        `cbor:"acl_groups"`
	Site               string          `cbor:"site"`
	KnownEndpoints     []KnownEndpoint `cbor:"known_endpoints"`
	IssuedAt           time.Time       `cbor:"issued_at"`
	ActivationDeadline time.Time       `cbor:"activation_deadline"`
	ExpiresAt          time.Time       `cbor:"expires_at"`
	Nonce              []byte          `cbor:"nonce"`
	CAID               string          `cbor:"ca_id"`

	SnitchProbeKey  []byte `cbor:"snitch_probe_key,omitempty"`
	TLSTrustAnchor  []byte `cbor:"tls_trust_anchor,omitempty"`
	CPDevicePubkey  []byte `cbor:"cp_device_pubkey,omitempty"`
	CPDevicePrivkey []byte `cbor:"cp_device_privkey,omitempty"`
	CPDeviceAddress string `cbor:"cp_device_address,omitempty"`

	Signature []byte `cbor:"signature,omitempty"`
}

func canonicalEnc() (cbor.EncMode, error) {
	return cbor.CanonicalEncOptions().EncMode()
}

// Signable returns the canonical CBOR encoding of the bundle with Signature
// cleared — the byte sequence the CA signs.
func (b *EnrollmentBundle) Signable() ([]byte, error) {
	em, err := canonicalEnc()
	if err != nil {
		return nil, fmt.Errorf("cbor canonical enc mode: %w", err)
	}
	unsigned := *b
	unsigned.Signature = nil
	return em.Marshal(&unsigned)
}

// Marshal returns the full canonical CBOR encoding including Signature.
func (b *EnrollmentBundle) Marshal() ([]byte, error) {
	em, err := canonicalEnc()
	if err != nil {
		return nil, fmt.Errorf("cbor canonical enc mode: %w", err)
	}
	return em.Marshal(b)
}

// Unmarshal parses CBOR-encoded bundle bytes. Caller must still Verify the
// signature and check expiry, activation deadline, and CRL.
func Unmarshal(data []byte) (*EnrollmentBundle, error) {
	var b EnrollmentBundle
	if err := cbor.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("cbor unmarshal: %w", err)
	}
	return &b, nil
}

// Verify checks the bundle's ECDSA P-256 signature against a trusted
// public key. Mirrors goat-trunk's bundle.EnrollmentBundle.Verify byte-
// for-byte: SHA-256 hash of the canonical CBOR payload + ASN.1-encoded
// ECDSA signature. Does not check expiry, activation deadline, or CRL —
// those are the caller's responsibility (CheckExpiry /
// CheckActivationDeadline).
func (b *EnrollmentBundle) Verify(pub *ecdsa.PublicKey) error {
	if pub == nil {
		return errors.New("public key is nil")
	}
	if pub.Curve == nil || pub.Curve.Params().Name != elliptic.P256().Params().Name {
		got := "nil"
		if pub.Curve != nil {
			got = pub.Curve.Params().Name
		}
		return fmt.Errorf("public key curve is %s, want P-256", got)
	}
	if len(b.Signature) == 0 {
		return errors.New("signature is empty")
	}
	payload, err := b.Signable()
	if err != nil {
		return fmt.Errorf("signable: %w", err)
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(pub, digest[:], b.Signature) {
		return errors.New("signature invalid")
	}
	return nil
}

// ErrExpired is returned when ExpiresAt is in the past.
var ErrExpired = errors.New("bundle expired")

// ErrActivationDeadlinePassed is returned when first enrollment is requested
// after the activation deadline. Independent of ExpiresAt.
var ErrActivationDeadlinePassed = errors.New("bundle activation deadline passed")

// CheckExpiry returns ErrExpired if now is after ExpiresAt.
func (b *EnrollmentBundle) CheckExpiry(now time.Time) error {
	if now.After(b.ExpiresAt) {
		return fmt.Errorf("%w: expires_at=%s now=%s", ErrExpired,
			b.ExpiresAt.UTC().Format(time.RFC3339),
			now.UTC().Format(time.RFC3339))
	}
	return nil
}

// CheckActivationDeadline returns ErrActivationDeadlinePassed if now is after
// ActivationDeadline.
func (b *EnrollmentBundle) CheckActivationDeadline(now time.Time) error {
	if now.After(b.ActivationDeadline) {
		return fmt.Errorf("%w: activation_deadline=%s now=%s", ErrActivationDeadlinePassed,
			b.ActivationDeadline.UTC().Format(time.RFC3339),
			now.UTC().Format(time.RFC3339))
	}
	return nil
}

// ErrCPDeviceKeypairUnpaired is returned when exactly one of CPDevicePubkey
// or CPDevicePrivkey is present.
var ErrCPDeviceKeypairUnpaired = errors.New("cp_device_pubkey/cp_device_privkey must be present together or both absent")

// CheckCPDeviceKeypair returns ErrCPDeviceKeypairUnpaired if exactly one
// of CPDevicePubkey or CPDevicePrivkey is present.
func (b *EnrollmentBundle) CheckCPDeviceKeypair() error {
	pub := len(b.CPDevicePubkey) > 0
	priv := len(b.CPDevicePrivkey) > 0
	if pub != priv {
		return ErrCPDeviceKeypairUnpaired
	}
	return nil
}

// Metadata is the GUI-side preview shape rendered by the bundle-import
// dialog. The full EnrollmentBundle carries fields (CPDevicePrivkey,
// SnitchProbeKey) that should never be displayed; Metadata is the
// safe subset.
type Metadata struct {
	IssuedTo   string
	Site       string
	NotBefore  time.Time
	NotAfter   time.Time
	PeerPubKey string
	Endpoints  []string
}

// Preview parses raw CBOR bundle bytes and returns the user-displayable
// Metadata. Does NOT verify the signature — the daemon's
// TrustRoots.VerifyBundle is the authoritative trust check, run after
// the user clicks Apply. Preview is for the dialog's "show me what's
// in here before I commit" step.
func Preview(raw []byte) (*Metadata, error) {
	if len(raw) == 0 {
		return nil, errors.New("bundle is empty")
	}
	b, err := Unmarshal(raw)
	if err != nil {
		return nil, err
	}
	eps := make([]string, 0, len(b.KnownEndpoints))
	for _, e := range b.KnownEndpoints {
		eps = append(eps, e.Addr)
	}
	return &Metadata{
		IssuedTo:   b.DeviceID,
		Site:       b.Site,
		NotBefore:  b.IssuedAt,
		NotAfter:   b.ExpiresAt,
		PeerPubKey: base64.StdEncoding.EncodeToString(b.PeerPubkey),
		Endpoints:  eps,
	}, nil
}
