package names

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store file names. The snapshot pair is the artifact byte-for-byte as
// signed plus its detached signature; observed-names.json is the
// noncanonical tier. All flat files: the signature (checked at every
// read) makes the canonical pair tamper-evident, and the observed tier
// is best-effort by design.
const (
	SnapshotFile = "name-snapshot.json"
	SigFile      = "name-snapshot.json.sig"
	ObservedFile = "observed-names.json"
)

// ObservedTTL prunes observations older than this — an observation this
// stale is no better than a guess.
const ObservedTTL = 30 * 24 * time.Hour

// ObservedMax bounds the observed store (newest-first cap).
const ObservedMax = 512

// Store is the shared on-disk name store (design §4.1). The daemon is
// the sole writer; any reader (goat-cli included) applies the same
// verify-at-read rule, so shared bytes carry no trust cost.
type Store struct {
	dir   string
	roots []*ecdsa.PublicKey
}

// NewStore opens (creating if needed) the store at dir, verifying
// against roots — the same trust roots that verify enrollment bundles.
// The dir is world-readable (0755) by design: the shared-store contract
// (design §4.1) has goat-cli reading the daemon's bytes, and on system
// installs the daemon runs as root/a service user. Nothing here is
// secret — the snapshot pair is served unauthenticated at get.<site>,
// and observed names are DNS-cache-equivalent; integrity comes from
// verify-at-read, not permissions. The chmod converges stores created
// 0700 by earlier releases.
func NewStore(dir string, roots []*ecdsa.PublicKey) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create names store dir: %w", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, fmt.Errorf("names store dir perms: %w", err)
	}
	return &Store{dir: dir, roots: roots}, nil
}

// Dir returns the store directory (for status surfaces).
func (st *Store) Dir() string { return st.dir }

// LoadSnapshot reads + verifies the cached snapshot. Every read
// re-verifies the signature — a tampered cache file is inert.
func (st *Store) LoadSnapshot() (*Snapshot, error) {
	artifact, err := os.ReadFile(filepath.Join(st.dir, SnapshotFile))
	if err != nil {
		return nil, fmt.Errorf("no cached snapshot (seeded at import or refreshed from get.<site> while names work): %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(st.dir, SigFile))
	if err != nil {
		return nil, fmt.Errorf("cached snapshot has no signature file: %w", err)
	}
	return VerifyAndParse(artifact, sig, st.roots)
}

// ErrNotNewer reports a validly-signed candidate refused because its
// serial does not exceed the cached one — the monotonic replay bound
// working as intended, not a failure.
var ErrNotNewer = errors.New("candidate serial not newer than cache — kept cache")

// PutSnapshot verifies a fetched artifact pair and stores it iff its
// serial is strictly newer than the cached one (or no valid cache
// exists) — the monotonic-serial replay bound. A validly-signed but
// not-newer candidate returns ErrNotNewer.
func (st *Store) PutSnapshot(artifact, sig []byte) (*Snapshot, error) {
	cand, err := VerifyAndParse(artifact, sig, st.roots)
	if err != nil {
		return nil, err
	}
	if cached, err := st.LoadSnapshot(); err == nil && cand.Serial <= cached.Serial {
		return nil, ErrNotNewer
	}
	// Signature first, artifact last: a torn write leaves an
	// unverifiable pair, which LoadSnapshot refuses — fail closed,
	// never a mismatched accept.
	if err := writeAtomic(filepath.Join(st.dir, SigFile), sig, 0o644); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(st.dir, SnapshotFile), artifact, 0o644); err != nil {
		return nil, err
	}
	return cand, nil
}

// RecordObservation upserts a live-learned name→IP binding into the
// observed tier, pruning expired entries and capping newest-first.
// Best-effort: I/O failures are returned but callers may ignore them.
func (st *Store) RecordObservation(name, ip string, now time.Time) error {
	want := strings.ToLower(strings.TrimSuffix(name, "."))
	records := st.loadObserved()
	kept := records[:0]
	for _, o := range records {
		if strings.ToLower(o.Name) == want {
			continue
		}
		if now.Sub(time.Unix(o.ObservedAt, 0)) >= ObservedTTL {
			continue
		}
		kept = append(kept, o)
	}
	kept = append(kept, ObservedRecord{Name: want, IP: ip, ObservedAt: now.Unix()})
	sort.Slice(kept, func(i, j int) bool { return kept[i].ObservedAt > kept[j].ObservedAt })
	if len(kept) > ObservedMax {
		kept = kept[:ObservedMax]
	}
	body, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(st.dir, ObservedFile), body, 0o644)
}

// LookupObserved returns the newest valid observation for name,
// TTL-pruned at read.
func (st *Store) LookupObserved(name string, now time.Time) *ObservedRecord {
	want := strings.ToLower(strings.TrimSuffix(name, "."))
	for _, o := range st.loadObserved() {
		if now.Sub(time.Unix(o.ObservedAt, 0)) >= ObservedTTL {
			continue
		}
		if strings.ToLower(o.Name) == want {
			rec := o
			return &rec
		}
	}
	return nil
}

// ObservedCount reports how many valid observations the tier holds (for
// status surfaces).
func (st *Store) ObservedCount(now time.Time) int {
	n := 0
	for _, o := range st.loadObserved() {
		if now.Sub(time.Unix(o.ObservedAt, 0)) < ObservedTTL {
			n++
		}
	}
	return n
}

func (st *Store) loadObserved() []ObservedRecord {
	b, err := os.ReadFile(filepath.Join(st.dir, ObservedFile))
	if err != nil {
		return nil
	}
	var records []ObservedRecord
	if json.Unmarshal(b, &records) != nil {
		return nil
	}
	return records
}

// writeAtomic writes via a temp file + rename in the same directory —
// the same pattern as internal/profile — so readers never see a partial
// file.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".names-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
