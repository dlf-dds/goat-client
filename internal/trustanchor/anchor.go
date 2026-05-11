package trustanchor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"fmt"
	"sort"
	"time"
)

// Anchor is one pinned offline-CA root public key with its validity window.
//
// PublicKey is the parsed *ecdsa.PublicKey on the P-256 curve (post-
// Block-79 cutover; mirrors goat-trunk's bundle.EnrollmentBundle.Verify
// signature). Name is the human-readable label that appears in audit
// logs ("dev-desertbread-ca-ecdsa-2026-05-09"). Issuer is the
// CA-identity string the offline-CA stamps into bundle.CAID — kept here
// so a successful Verify can be cross-checked against bundle.CAID by the
// caller without re-deriving the issuer from the key bytes.
//
// SPKI is the raw SubjectPublicKeyInfo (DER) that the embedded data
// carries; PublicKey is populated by NewSet via x509.ParsePKIXPublicKey.
// The SPKI bytes are retained so callers can dump or re-emit the
// canonical on-disk form (the generator emits SPKI rather than
// reconstructed X/Y big.Int literals because SPKI is the substrate's
// canonical wire form).
type Anchor struct {
	Name       string
	Issuer     string
	PublicKey  *ecdsa.PublicKey
	SPKI       []byte
	ValidFrom  time.Time
	ValidUntil time.Time
}

// active reports whether at is within [ValidFrom, ValidUntil] inclusive.
// Comparison is done in UTC — the YAML descriptor stores all dates in
// UTC, so wall-clock skew on the device side is the only source of
// drift.
func (a *Anchor) active(at time.Time) bool {
	at = at.UTC()
	if at.Before(a.ValidFrom.UTC()) {
		return false
	}
	if at.After(a.ValidUntil.UTC()) {
		return false
	}
	return true
}

// Set is the collection of anchors compiled into the binary. Construct
// the production set with [Default]; tests construct synthetic sets with
// [NewSet].
//
// Set is immutable after construction. Verify is safe for concurrent
// use.
type Set struct {
	anchors []Anchor
	now     func() time.Time
}

// NewSet returns a Set containing the supplied anchors. Returns an
// error if any anchor has an empty Name, a missing PublicKey + SPKI
// pair, a non-P-256 curve, or a ValidUntil that does not strictly
// follow ValidFrom — caller bugs that would otherwise silently produce
// a Set that can never verify.
//
// If PublicKey is nil and SPKI is supplied, NewSet parses the SPKI
// into PublicKey (the path used by the embedded.go generator). If
// PublicKey is supplied directly (test helpers), SPKI may be empty
// and the curve check runs against PublicKey.Curve.
//
// The order of anchors is preserved; Verify iterates in insertion order.
func NewSet(anchors ...Anchor) (*Set, error) {
	out := make([]Anchor, 0, len(anchors))
	for i, a := range anchors {
		if a.Name == "" {
			return nil, fmt.Errorf("anchor %d: name is empty", i)
		}
		if a.PublicKey == nil {
			if len(a.SPKI) == 0 {
				return nil, fmt.Errorf("anchor %q: PublicKey and SPKI both empty", a.Name)
			}
			pub, err := parseSPKI(a.SPKI)
			if err != nil {
				return nil, fmt.Errorf("anchor %q: parse SPKI: %w", a.Name, err)
			}
			a.PublicKey = pub
		}
		if a.PublicKey.Curve == nil || a.PublicKey.Curve.Params().Name != elliptic.P256().Params().Name {
			got := "nil"
			if a.PublicKey.Curve != nil {
				got = a.PublicKey.Curve.Params().Name
			}
			return nil, fmt.Errorf("anchor %q: ECDSA pubkey curve is %s, want P-256", a.Name, got)
		}
		if !a.ValidUntil.After(a.ValidFrom) {
			return nil, fmt.Errorf("anchor %q: valid_until (%s) must be strictly after valid_from (%s)",
				a.Name,
				a.ValidUntil.UTC().Format(time.RFC3339),
				a.ValidFrom.UTC().Format(time.RFC3339))
		}
		out = append(out, a)
	}
	return &Set{
		anchors: out,
		now:     time.Now,
	}, nil
}

// parseSPKI decodes SubjectPublicKeyInfo (DER) bytes into an ECDSA P-256
// public key. Used by NewSet to lazily inflate embedded anchors.
func parseSPKI(spki []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("SPKI is %T, want *ecdsa.PublicKey", pub)
	}
	return ec, nil
}

// Default returns the build-time-embedded Set of pinned anchors.
//
// Panics if the embedded data is malformed — that condition can only
// arise from a corrupted code-generator run, which would also fail the
// build tests that exercise this constructor. Callers do not need to
// guard against the panic at runtime.
func Default() *Set {
	s, err := NewSet(embedded...)
	if err != nil {
		panic(fmt.Sprintf("trustanchor: embedded anchors malformed: %v", err))
	}
	return s
}

// withClock returns a copy of s whose Verify uses fn instead of
// time.Now. Test-only.
func (s *Set) withClock(fn func() time.Time) *Set {
	cp := *s
	cp.now = fn
	return &cp
}

// All returns a copy of every anchor in the set, regardless of validity.
// Useful for diagnostics ("which anchors does this binary carry?").
func (s *Set) All() []Anchor {
	return append([]Anchor(nil), s.anchors...)
}

// Active returns the anchors whose [ValidFrom, ValidUntil] window
// contains at, sorted by ValidFrom ascending so the oldest still-valid
// anchor appears first. The returned slice is a fresh copy.
func (s *Set) Active(at time.Time) []Anchor {
	var out []Anchor
	for _, a := range s.anchors {
		if a.active(at) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ValidFrom.Before(out[j].ValidFrom)
	})
	return out
}
