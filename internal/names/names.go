// Package names implements the device-wide half of the goat name fallback
// resolver (DesertBreadBird ADR 1082 + docs/design/name-fallback-resolver.md).
//
// Failure class: node network up, mesh name service down or unreachable —
// every mesh service dies *by name* while the WireGuard paths still work.
// The zone authority (the per-goatnet offline CA — the same key that signs
// enrollment bundles) signs a serial-versioned, TTL-declared snapshot of
// the registry-declared name set. This package:
//
//   - owns the shared on-disk store of that artifact (flat files,
//     signature verified at every read — tamper-evident, so no database
//     and no store-guarding service);
//   - refreshes it opportunistically from get.<site>.<zone> while names
//     work (monotonic-serial acceptance: a lower serial never replaces a
//     higher one — the replay bound);
//   - maintains the NONCANONICAL observed tier: successful live
//     resolutions are remembered so live-only records (peer names,
//     operator hotfixes the registry never captured) survive an outage,
//     always labeled ad hoc, never blended with canonical answers;
//   - answers A-record queries through a local UDP forwarder: live
//     upstream first (never shadowed), signed snapshot second, observed
//     records last.
//
// Trust lives entirely in the artifact signature (ECDSA P-256 over the
// exact file bytes, verified against the same trust roots the daemon uses
// for enrollment bundles). Grading is fail-closed: a snapshot past its
// declared TTL or with a bad signature is refused, never silently served.
package names

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// SnapshotFormat is the artifact format this package understands.
const SnapshotFormat = "goat-name-snapshot-v1"

// Grade is the fail-closed staleness classification of a snapshot.
type Grade string

const (
	// GradeFresh — younger than FreshBound; routine.
	GradeFresh Grade = "fresh"
	// GradeAging — older than FreshBound but inside the declared TTL;
	// served with an explicit staleness warning.
	GradeAging Grade = "aging"
	// GradeExpired — at or past the declared TTL; never served.
	GradeExpired Grade = "expired"
)

// FreshBound is the fresh/aging boundary. Under a week reads as routine;
// older warns.
const FreshBound = 7 * 24 * time.Hour

// Snapshot is the signed name-set artifact (see the DesertBreadBird
// generator ops/dns/generate-mesh-hosts.py — one registry, two renderings).
type Snapshot struct {
	Format          string   `json:"format"`
	SiteID          string   `json:"site_id"`
	Zone            string   `json:"zone"`
	Serial          uint64   `json:"serial"`
	GeneratedAt     string   `json:"generated_at"`
	GeneratedAtUnix int64    `json:"generated_at_unix"`
	TTLSeconds      uint64   `json:"ttl_seconds"`
	CAID            string   `json:"ca_id"`
	Records         []Record `json:"records"`
}

// Record is one canonical name→IP binding from the snapshot.
type Record struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// ObservedRecord is one entry of the noncanonical observed tier: a
// name→IP binding learned from a successful live resolution, with its
// provenance timestamp.
type ObservedRecord struct {
	Name       string `json:"name"`
	IP         string `json:"ip"`
	ObservedAt int64  `json:"observed_at"`
}

// Meta labels a snapshot-sourced answer: serial + age + grade — the
// "names as of serial N, age M" honesty contract.
type Meta struct {
	Serial  uint64
	Age     time.Duration
	Grade   Grade
	SiteID  string
	Zone    string
	Records int
}

// GradeAt classifies the snapshot's staleness against now. A zero or
// negative GeneratedAtUnix is never servable.
func (s *Snapshot) GradeAt(now time.Time) Meta {
	age := now.Sub(time.Unix(s.GeneratedAtUnix, 0))
	g := GradeFresh
	switch {
	case s.GeneratedAtUnix <= 0 || age >= time.Duration(s.TTLSeconds)*time.Second:
		g = GradeExpired
	case age >= FreshBound:
		g = GradeAging
	}
	if age < 0 {
		age = 0
	}
	return Meta{
		Serial:  s.Serial,
		Age:     age,
		Grade:   g,
		SiteID:  s.SiteID,
		Zone:    s.Zone,
		Records: len(s.Records),
	}
}

// Lookup returns the canonical record for name (case-insensitive,
// trailing-dot tolerant), if any.
func (s *Snapshot) Lookup(name string) (Record, bool) {
	want := strings.ToLower(strings.TrimSuffix(name, "."))
	for _, r := range s.Records {
		if strings.ToLower(r.Name) == want {
			return r, true
		}
	}
	return Record{}, false
}

// ErrBadSignature is returned when the detached signature does not verify
// against any trust root — wrong CA or tampered file.
var ErrBadSignature = errors.New("snapshot signature invalid — wrong CA or tampered file")

// VerifyAndParse checks the detached signature (base64 of an ASN.1 DER
// ECDSA-Sig-Value over SHA-256 of the exact artifact bytes) against the
// given trust roots — the same roots that verify enrollment bundles —
// then parses and shape-checks the artifact. Fail-closed: no roots, bad
// base64/DER, wrong format, or no verifying root all refuse.
func VerifyAndParse(artifact, sig []byte, roots []*ecdsa.PublicKey) (*Snapshot, error) {
	if len(roots) == 0 {
		return nil, errors.New("no trust roots — refusing to verify snapshot")
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return nil, fmt.Errorf("signature not base64: %w", err)
	}
	digest := sha256.Sum256(artifact)
	verified := false
	for _, pub := range roots {
		if pub != nil && ecdsa.VerifyASN1(pub, digest[:], der) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, ErrBadSignature
	}
	var snap Snapshot
	if err := json.Unmarshal(artifact, &snap); err != nil {
		return nil, fmt.Errorf("snapshot malformed: %w", err)
	}
	if snap.Format != SnapshotFormat {
		return nil, fmt.Errorf("unexpected snapshot format %q", snap.Format)
	}
	return &snap, nil
}

// Source says where an answer came from — surfaced verbatim so an
// operator on fallback names always sees it.
type Source string

const (
	// SourceLive — the upstream resolver answered.
	SourceLive Source = "live"
	// SourceSnapshot — the signed snapshot answered (labeled serial+age).
	SourceSnapshot Source = "snapshot"
	// SourceObserved — the NONCANONICAL observed tier answered (ad hoc).
	SourceObserved Source = "observed"
)

// Answer is one resolved name with its provenance.
type Answer struct {
	IP     netip.Addr
	Source Source
	// Snapshot metadata when Source == SourceSnapshot.
	Meta *Meta
	// Age of the observation when Source == SourceObserved.
	ObservedAge time.Duration
}

// PickFallback is the pure fallback decision, shared by the forwarder and
// unit-testable in isolation. snapErr carries why the snapshot tier could
// not be consulted (missing/unverifiable); a non-nil snap past TTL is
// refused here. Rules (design §4.2): canonical snapshot wins; a FRESHER
// observation supersedes a conflicting snapshot record (the
// operator-hotfix case); observations gap-fill names the snapshot never
// had; the observed tier also serves when the snapshot tier cannot.
func PickFallback(snap *Snapshot, snapErr error, obs *ObservedRecord, name string, now time.Time) (Answer, error) {
	var snapReason string
	switch {
	case snapErr != nil:
		snapReason = snapErr.Error()
	case snap != nil:
		meta := snap.GradeAt(now)
		if meta.Grade == GradeExpired {
			snapReason = fmt.Sprintf("snapshot expired (serial %d, ttl %ds) — refusing stale names", snap.Serial, snap.TTLSeconds)
		} else if rec, ok := snap.Lookup(name); ok {
			if obs != nil && obs.IP != rec.IP && obs.ObservedAt > snap.GeneratedAtUnix {
				return observedAnswer(obs, now)
			}
			ip, err := netip.ParseAddr(rec.IP)
			if err != nil {
				return Answer{}, fmt.Errorf("snapshot record for %s has malformed ip %q: %w", name, rec.IP, err)
			}
			return Answer{IP: ip, Source: SourceSnapshot, Meta: &meta}, nil
		} else {
			if obs != nil {
				return observedAnswer(obs, now)
			}
			snapReason = fmt.Sprintf("no record for %s in snapshot serial %d", name, snap.Serial)
		}
	default:
		snapReason = "no snapshot"
	}
	if obs != nil {
		return observedAnswer(obs, now)
	}
	return Answer{}, fmt.Errorf("%s; no observed record for %s", snapReason, name)
}

func observedAnswer(obs *ObservedRecord, now time.Time) (Answer, error) {
	ip, err := netip.ParseAddr(obs.IP)
	if err != nil {
		return Answer{}, fmt.Errorf("observed record for %s has malformed ip %q: %w", obs.Name, obs.IP, err)
	}
	age := now.Sub(time.Unix(obs.ObservedAt, 0))
	if age < 0 {
		age = 0
	}
	return Answer{IP: ip, Source: SourceObserved, ObservedAge: age}, nil
}

// AgeHuman renders a compact human age: "42m", "7h", "3d".
func AgeHuman(d time.Duration) string {
	s := int64(d.Seconds())
	if s < 0 {
		s = 0
	}
	switch {
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	default:
		return fmt.Sprintf("%dd", s/86400)
	}
}
