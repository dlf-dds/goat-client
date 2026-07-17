package names

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Self-claims tier (DesertBreadBird ADR 1082 Amendment 2, design §7).
//
// A peer may assert only ITS OWN name→IP binding, signed under its own
// leaf cert. Verification is fully offline: the leaf chains to the same
// trust roots that verify enrollment bundles and snapshots, and the
// cached verified snapshot's peer_bindings table says which anchor owns
// which name — so own-name-only and never-override-service-records hold
// by construction (service names carry no binding). Claims slot between
// live DNS and the snapshot, for peer names only; the observed tier is
// demoted by precedence wherever a valid claim answers.

// ClaimsFile is the third store file: claim envelopes collected from
// the fabric while healthy (get.<site>/claims.json), served while broken.
const ClaimsFile = "claims.json"

// ClaimFormat is the artifact format this package understands.
const ClaimFormat = "goat-name-claim-v1"

// ClaimArtifact is the signed payload — the canonical bytes are what the
// leaf signs; wire fields match the goat-cli implementation.
type ClaimArtifact struct {
	Format       string `json:"format"`
	Name         string `json:"name"`
	IP           string `json:"ip"`
	Anchor       string `json:"anchor"`
	IssuedAtUnix int64  `json:"issued_at_unix"`
	TTLSeconds   uint64 `json:"ttl_seconds"`
}

// ClaimEnvelope wraps the exact artifact bytes (base64) + the leaf ECDSA
// signature over them + the leaf cert. Every transport (fabric,
// collector, get tier, store file) is untrusted — verify at read.
type ClaimEnvelope struct {
	ArtifactB64 string `json:"artifact_b64"`
	SigB64      string `json:"sig_b64"`
	LeafPEM     string `json:"leaf_pem"`
}

// ValidClaim is a claim that survived full verification, ready to serve.
type ValidClaim struct {
	Name string
	IP   netip.Addr
	Age  time.Duration
}

var anchorShaped = regexp.MustCompile(`^[0-9a-f]{32}$`)

// VerifyClaim runs the full offline check against the CACHED VERIFIED
// snapshot: leaf chains to a trust root + leaf validity window + artifact
// signature under the leaf key + leaf identity equals the claimed anchor
// + the snapshot binds the name to that anchor + TTL freshness. Every
// refusal fails closed with a human reason.
func VerifyClaim(env *ClaimEnvelope, snap *Snapshot, roots []*ecdsa.PublicKey, now time.Time) (*ValidClaim, error) {
	if len(roots) == 0 {
		return nil, errors.New("no trust roots — refusing to verify claim")
	}
	block, _ := pem.Decode([]byte(env.LeafPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("leaf is not a CERTIFICATE PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("leaf not X.509: %w", err)
	}
	tbsDigest := sha256.Sum256(leaf.RawTBSCertificate)
	rooted := false
	for _, root := range roots {
		if root != nil && ecdsa.VerifyASN1(root, tbsDigest[:], leaf.Signature) {
			rooted = true
			break
		}
	}
	if !rooted {
		return nil, errors.New("leaf not signed by a trust root")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("leaf outside its validity window (%s..%s)",
			leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	artifact, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.ArtifactB64))
	if err != nil {
		return nil, fmt.Errorf("artifact not base64: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.SigB64))
	if err != nil {
		return nil, fmt.Errorf("claim sig not base64: %w", err)
	}
	leafKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("leaf key is not ECDSA")
	}
	artDigest := sha256.Sum256(artifact)
	if !ecdsa.VerifyASN1(leafKey, artDigest[:], sig) {
		return nil, errors.New("claim signature invalid under the leaf key")
	}

	var claim ClaimArtifact
	if err := json.Unmarshal(artifact, &claim); err != nil {
		return nil, fmt.Errorf("claim malformed: %w", err)
	}
	if claim.Format != ClaimFormat {
		return nil, fmt.Errorf("unexpected claim format %q", claim.Format)
	}
	leafID := leafIdentity(leaf)
	if leafID == "" {
		return nil, errors.New("leaf carries no goat identity (CN anchor / URI-SAN org=/vendor=)")
	}
	if leafID != claim.Anchor {
		return nil, fmt.Errorf("leaf identity %q does not match claimed anchor %q", leafID, claim.Anchor)
	}

	wanted := strings.ToLower(strings.TrimSuffix(claim.Name, "."))
	var bound *PeerBinding
	for i := range snap.PeerBindings {
		if strings.ToLower(snap.PeerBindings[i].Name) == wanted {
			bound = &snap.PeerBindings[i]
			break
		}
	}
	if bound == nil {
		return nil, fmt.Errorf("snapshot serial %d has no binding for %s", snap.Serial, claim.Name)
	}
	if bound.Anchor != claim.Anchor {
		return nil, fmt.Errorf("name %s is bound to a different anchor — refusing cross-claim", claim.Name)
	}

	age := now.Sub(time.Unix(claim.IssuedAtUnix, 0))
	if claim.IssuedAtUnix <= 0 || age >= time.Duration(claim.TTLSeconds)*time.Second {
		return nil, fmt.Errorf("claim expired (age %s ≥ ttl %ds) — refusing stale claim", age.Truncate(time.Second), claim.TTLSeconds)
	}
	ip, err := netip.ParseAddr(claim.IP)
	if err != nil {
		return nil, fmt.Errorf("claim ip %q malformed: %w", claim.IP, err)
	}
	if age < 0 {
		age = 0
	}
	return &ValidClaim{Name: wanted, IP: ip, Age: age}, nil
}

// leafIdentity extracts the goat identity per the certmint vocabulary:
// slot-mode CN (bare anchor, 32 lowercase hex) first, else the URI-SAN
// org=/vendor= value.
func leafIdentity(leaf *x509.Certificate) string {
	if cn := leaf.Subject.CommonName; anchorShaped.MatchString(cn) {
		return cn
	}
	for _, uri := range leaf.URIs {
		s := uri.String()
		for _, key := range []string{"org=", "vendor="} {
			if pos := strings.Index(s, key); pos >= 0 {
				if v := s[pos+len(key):]; v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// LookupClaim returns the freshest valid claim for name, verified at
// read against the given snapshot + the store's trust roots. Missing or
// malformed claims files are silently no-claims — resolution falls
// through to the snapshot/observed tiers, which label themselves.
func (st *Store) LookupClaim(name string, snap *Snapshot, now time.Time) *ValidClaim {
	b, err := os.ReadFile(filepath.Join(st.dir, ClaimsFile))
	if err != nil {
		return nil
	}
	var envs []ClaimEnvelope
	if json.Unmarshal(b, &envs) != nil {
		return nil
	}
	wanted := strings.ToLower(strings.TrimSuffix(name, "."))
	var best *ValidClaim
	for i := range envs {
		vc, err := VerifyClaim(&envs[i], snap, st.roots, now)
		if err != nil || vc.Name != wanted {
			continue
		}
		if best == nil || vc.Age < best.Age {
			best = vc
		}
	}
	return best
}

// PutClaims stores a fetched claims.json after syntactic validation.
// No serial semantics — each claim is individually leaf-signed and
// verified at read, so replacing the file wholesale is safe (a bad
// courier can deny, never forge).
func (st *Store) PutClaims(body []byte) error {
	var envs []ClaimEnvelope
	if err := json.Unmarshal(body, &envs); err != nil {
		return fmt.Errorf("claims.json malformed: %w", err)
	}
	return writeAtomic(filepath.Join(st.dir, ClaimsFile), body, 0o644)
}

// ClaimCount reports how many envelopes the store holds (syntactic count
// for status surfaces — validity depends on the snapshot at read time).
func (st *Store) ClaimCount() int {
	b, err := os.ReadFile(filepath.Join(st.dir, ClaimsFile))
	if err != nil {
		return 0
	}
	var envs []ClaimEnvelope
	if json.Unmarshal(b, &envs) != nil {
		return 0
	}
	return len(envs)
}
