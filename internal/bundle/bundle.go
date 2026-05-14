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
	// KindRelay matches the goat-trunk bundle-create canonical value
	// "cp-relay" (the wg-cp0 outer-tunnel relay kind). The earlier
	// "relay" string in this mirror was a schema-drift bug:
	// ops/enrollment/cmd/bundle-create/ has always emitted "cp-relay",
	// so every production bundle silently failed FromBundle with
	// ErrNoEndpoint until this constant was reconciled.
	KindRelay Kind = "cp-relay"
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
	// the wg-cp0 conf renderer always derives `MeshAddr/32` as the
	// first AllowedIPs entry from this.
	MeshAddr string `cbor:"mesh_addr,omitempty"`

	// AllowedIPs, when non-empty, are ADDED to the renderer's derived
	// `MeshAddr/32` entry (additive, not overriding). Each entry is a
	// CIDR string. Bundle-issuance side populates this from the
	// operator flag --first-relay-route-subnet. Matches the canonical
	// `wg-cp0-bundle-apply` renderer: a relay peer with MeshAddr
	// 198.18.0.3 and AllowedIPs ["198.18.0.0/24"] renders to
	// `AllowedIPs = 198.18.0.3/32, 198.18.0.0/24`.
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

	// InnerMeshSetup is the v0.2 extension carrying mgmt URL + setup
	// key (+ optional admin token + PSK) for the netbird inner mesh.
	// Populated when the issuing operator wants the device to run in
	// `netbird-only` or `combined` mode (per ADR 0840 Amendment
	// 2026-05-13). Absent for v0.1.x-shape bundles — the parser
	// treats absent as "wg-cp0-only-eligible only."
	//
	// The CBOR encoder omits the field when all sub-fields are zero
	// (omitempty on the struct value), so v1 bundles whose issuer did
	// not populate this field round-trip byte-identically through
	// new parser code. The wire-compat property is asserted in
	// TestV0_2FieldsOmitWhenEmpty so v0.1.x bundles in the wild
	// continue to verify after this rolls out.
	InnerMeshSetup InnerMeshSetup `cbor:"inner_mesh_setup,omitempty"`

	// MobileCert is the Block 80F per-device mTLS client cert + key
	// (PEM-bundled, leaf cert first, any intermediate chain, then
	// the private key block). Used when the device reaches mgmt +
	// signal + relay through the Block 80 / ADR 0843 public-mTLS
	// crutch tier in `netbird-only` mode. Absent for v0.1.x bundles
	// and for devices that reach the inner mesh directly without the
	// crutch.
	MobileCert []byte `cbor:"mobile_cert,omitempty"`

	Signature []byte `cbor:"signature,omitempty"`
}

// InnerMeshSetup is the v0.2 nested-field shape carried inside
// EnrollmentBundle. Mirrors goat-trunk's bundle schema; the
// Go-side stable shape lives in internal/innermesh and is derived
// from this via innermesh.FromBundle.
//
// Fields with zero values are omitted on the wire (the parent
// struct's omitempty drops the whole nested map when all sub-fields
// are zero). The inner-mesh eligibility test is
// (*EnrollmentBundle).HasInnerMesh — true when ManagementURL and
// SetupKey are both non-empty.
type InnerMeshSetup struct {
	// ManagementURL is the netbird management plane endpoint. Must
	// be https. Examples:
	//   https://mgmt.example.internal
	//   https://mgmt.example.internal:33073
	//   https://mgmt-crutch.example.public  (Block 80 crutch tier)
	ManagementURL string `cbor:"management_url,omitempty"`

	// SetupKey is the netbird setup-key string presented on first
	// registration.
	SetupKey string `cbor:"setup_key,omitempty"`

	// AdminAccessToken is the optional bearer token for the netbird
	// management API. Surface used by the diagnostics IPC method.
	AdminAccessToken string `cbor:"admin_access_token,omitempty"`

	// PreSharedKey is the optional WireGuard PSK passed through to
	// the inner-mesh userspace device. 32 bytes when set.
	PreSharedKey []byte `cbor:"pre_shared_key,omitempty"`
}

// HasInnerMesh reports whether the bundle carries enough information
// to attempt the netbird inner mesh (`netbird-only` or `combined`
// modes). Tests the InnerMeshSetup nested struct rather than the
// presence of the CBOR field — both encodings (absent map / map with
// zero values) collapse to the same Go shape after Unmarshal, which
// is the property `omitempty` gives us.
func (b *EnrollmentBundle) HasInnerMesh() bool {
	return b.InnerMeshSetup.ManagementURL != "" && b.InnerMeshSetup.SetupKey != ""
}

// HasWgCp0 reports whether the bundle carries enough information to
// attempt the wg-cp0 outer tunnel (`wg-cp0-only` or `combined`
// modes). The CP device keypair must be present and paired, and the
// device address must be set.
func (b *EnrollmentBundle) HasWgCp0() bool {
	if err := b.CheckCPDeviceKeypair(); err != nil {
		return false
	}
	return len(b.CPDevicePubkey) == 32 &&
		len(b.CPDevicePrivkey) == 32 &&
		b.CPDeviceAddress != ""
}

// HasMobileCert reports whether the bundle carries a Block 80F
// per-device mTLS client cert + key. Required when the bundle's
// inner-mesh ManagementURL targets the Block 80 crutch tier.
func (b *EnrollmentBundle) HasMobileCert() bool {
	return len(b.MobileCert) > 0
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
