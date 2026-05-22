package bundle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// ErrNoTrustRoots is returned when verification is attempted with an empty
// TrustRoots set — fail-closed so a misconfigured daemon never accepts an
// unsigned bundle.
var ErrNoTrustRoots = errors.New("no trust roots configured")

// ErrUntrustedBundle is returned when the bundle's signature does not match
// any of the configured trust roots.
var ErrUntrustedBundle = errors.New("bundle signature does not match any trust root")

// TrustRoots is the set of ECDSA P-256 public keys that a daemon will
// accept as signers of inbound bundles. A daemon typically loads exactly
// one root — the offline CA's bundle-signing pubkey — but the slice form
// supports CA rotation: during a rotation window both old and new roots
// are pinned, and bundles signed by either are accepted.
//
// Wire format on disk: PEM-encoded SubjectPublicKeyInfo blocks
// (`-----BEGIN PUBLIC KEY-----`) OR X.509 CERTIFICATE blocks
// (`-----BEGIN CERTIFICATE-----`) carrying ECDSA P-256 keys, one block
// per root, concatenated. Accepting both formats mirrors
// goat-trunk's wg-cp0-bundle-apply loadCAPubkey (commit dc3944fe) —
// operators can point at either `ops/enrollment/public-keys/<ca-id>.pem`
// (raw SPKI) or `ops/enrollment/ca/dev/root-cert.pem` (X.509 cert
// wrapping the same key, also the Traefik chain anchor) without hitting
// the "expected PUBLIC KEY, got CERTIFICATE" footgun.
type TrustRoots struct {
	keys []*ecdsa.PublicKey
}

// NewTrustRoots constructs a TrustRoots set from ECDSA P-256 public keys.
// Returns an error if any key is nil or on a non-P-256 curve.
func NewTrustRoots(keys ...*ecdsa.PublicKey) (*TrustRoots, error) {
	tr := &TrustRoots{}
	for i, k := range keys {
		if k == nil {
			return nil, fmt.Errorf("trust root %d: nil public key", i)
		}
		if k.Curve == nil || k.Curve.Params().Name != elliptic.P256().Params().Name {
			got := "nil"
			if k.Curve != nil {
				got = k.Curve.Params().Name
			}
			return nil, fmt.Errorf("trust root %d: curve is %s, want P-256", i, got)
		}
		tr.keys = append(tr.keys, k)
	}
	return tr, nil
}

// Add appends a P-256 public key to an existing TrustRoots set.
// Returns the same curve / nil validation errors as NewTrustRoots.
//
// Mostly useful for test scaffolding (multi-bundle tests minting a
// fresh key per bundle); production trust sets are constructed once
// at daemon start-up via NewTrustRoots / LoadTrustRootsFromFile.
// Not safe for concurrent use; callers serialize.
func (tr *TrustRoots) Add(k *ecdsa.PublicKey) error {
	if k == nil {
		return fmt.Errorf("trust root: nil public key")
	}
	if k.Curve == nil || k.Curve.Params().Name != elliptic.P256().Params().Name {
		got := "nil"
		if k.Curve != nil {
			got = k.Curve.Params().Name
		}
		return fmt.Errorf("trust root: curve is %s, want P-256", got)
	}
	tr.keys = append(tr.keys, k)
	return nil
}

// LoadTrustRootsFromFile reads a PEM file containing one or more
// `PUBLIC KEY` or `CERTIFICATE` blocks and returns the TrustRoots they
// encode. Each block must carry an ECDSA P-256 key.
func LoadTrustRootsFromFile(path string) (*TrustRoots, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trust roots: %w", err)
	}
	return LoadTrustRootsFromPEM(data)
}

// LoadTrustRootsFromPEM parses one or more `PUBLIC KEY` or `CERTIFICATE`
// PEM blocks. CERTIFICATE blocks have their SubjectPublicKeyInfo
// extracted; the cert chain itself is not validated (the offline-CA root
// is self-signed by definition).
func LoadTrustRootsFromPEM(data []byte) (*TrustRoots, error) {
	var keys []*ecdsa.PublicKey
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		pub, err := publicKeyFromPEMBlock(block)
		if err != nil {
			// Skip unknown block types silently (e.g. comments,
			// "EC PARAMETERS"); fail only on parse errors inside
			// the expected block types.
			if errors.Is(err, errSkipBlock) {
				continue
			}
			return nil, err
		}
		keys = append(keys, pub)
	}
	if len(keys) == 0 {
		return nil, errors.New("no PUBLIC KEY or CERTIFICATE blocks carrying ECDSA P-256 keys found")
	}
	return &TrustRoots{keys: keys}, nil
}

// errSkipBlock signals a PEM block whose type we don't recognise; the
// loader skips it rather than failing the whole file (handles concatenated
// files that contain ancillary blocks like `EC PARAMETERS`).
var errSkipBlock = errors.New("pem block skipped")

// publicKeyFromPEMBlock extracts an *ecdsa.PublicKey from either a
// `PUBLIC KEY` (SPKI) block or a `CERTIFICATE` (X.509) block. Mirrors
// the loadCAPubkey switch in goat-trunk dc3944fe.
func publicKeyFromPEMBlock(block *pem.Block) (*ecdsa.PublicKey, error) {
	switch block.Type {
	case "PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PUBLIC KEY: %w", err)
		}
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("PUBLIC KEY is %T, want *ecdsa.PublicKey", pub)
		}
		if ecPub.Curve == nil || ecPub.Curve.Params().Name != elliptic.P256().Params().Name {
			got := "nil"
			if ecPub.Curve != nil {
				got = ecPub.Curve.Params().Name
			}
			return nil, fmt.Errorf("ECDSA pubkey curve is %s, want P-256", got)
		}
		return ecPub, nil
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CERTIFICATE: %w", err)
		}
		ecPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("certificate carries %T, want *ecdsa.PublicKey", cert.PublicKey)
		}
		if ecPub.Curve == nil || ecPub.Curve.Params().Name != elliptic.P256().Params().Name {
			got := "nil"
			if ecPub.Curve != nil {
				got = ecPub.Curve.Params().Name
			}
			return nil, fmt.Errorf("certificate pubkey curve is %s, want P-256", got)
		}
		return ecPub, nil
	default:
		return nil, errSkipBlock
	}
}

// Empty reports whether the trust set has no keys.
func (tr *TrustRoots) Empty() bool {
	return tr == nil || len(tr.keys) == 0
}

// VerifyBundle checks the bundle's signature against every pinned root and
// returns nil on first match. Returns ErrNoTrustRoots if the set is empty
// and ErrUntrustedBundle if no root matches.
func (tr *TrustRoots) VerifyBundle(b *EnrollmentBundle) error {
	if tr.Empty() {
		return ErrNoTrustRoots
	}
	var lastErr error
	for _, k := range tr.keys {
		if err := b.Verify(k); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w: %v", ErrUntrustedBundle, lastErr)
	}
	return ErrUntrustedBundle
}
