package trustanchor

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
)

// ErrUntrusted is returned by Verify when no active anchor's public key
// validates the supplied signature.
var ErrUntrusted = errors.New("trustanchor: signature not signed by any active anchor")

// ErrNoActiveAnchors is returned by Verify when the Set contains no
// anchor whose validity window covers the current wall-clock — every
// pinned anchor has either not yet started or has already expired.
//
// Callers should treat this as a stale-binary signal: the daemon can
// neither sign nor accept bundles until it is upgraded.
var ErrNoActiveAnchors = errors.New("trustanchor: no active anchor at current time (binary is stale)")

// ErrEmptySignature is returned when the supplied signature is empty.
// ASN.1 ECDSA signatures vary in length (≈70-72 bytes typical for
// P-256), so we no longer enforce a fixed size — only that something
// is present for ecdsa.VerifyASN1 to consume.
var ErrEmptySignature = errors.New("trustanchor: signature is empty")

// Verify checks bundleSig as an ECDSA P-256 ASN.1-encoded signature
// over SHA-256 of bundleBytes against every anchor active at the
// current wall-clock. Returns the matching anchor on success, or one
// of [ErrNoActiveAnchors], [ErrEmptySignature], [ErrUntrusted].
//
// bundleBytes must be the canonical-CBOR signable payload — typically
// the output of internal/bundle.EnrollmentBundle.Signable. This package
// is intentionally agnostic to the bundle wire format so that a future
// signed-channel-update payload (ADR 0840 D5 v2) can reuse the same
// anchor set.
//
// Mirrors goat-trunk's bundle.EnrollmentBundle.Verify byte-for-byte
// (Block 79 cutover, commit 3dd4a765): SHA-256 the payload, ASN.1
// ECDSA verify against the P-256 pubkey.
//
// Rotation behaviour: when the Set carries an old + new anchor whose
// windows overlap, both are tried in insertion order. The returned
// anchor identifies which key the bundle was actually signed under so
// the caller can record the active CA-id in audit logs.
func (s *Set) Verify(bundleSig []byte, bundleBytes []byte) (*Anchor, error) {
	if len(bundleSig) == 0 {
		return nil, ErrEmptySignature
	}
	now := s.now().UTC()
	hasActive := false
	digest := sha256.Sum256(bundleBytes)
	for i := range s.anchors {
		a := &s.anchors[i]
		if !a.active(now) {
			continue
		}
		hasActive = true
		if ecdsa.VerifyASN1(a.PublicKey, digest[:], bundleSig) {
			return a, nil
		}
	}
	if !hasActive {
		return nil, ErrNoActiveAnchors
	}
	return nil, ErrUntrusted
}
