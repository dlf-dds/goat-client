// Package profile is the multi-network store for goat-client (Block 76M).
//
// A Profile binds a verified offline-CA-signed bundle to a per-profile
// Mode. The store holds N profiles + a single active-profile pointer.
// Switching active profile tears the previously-active legs down and
// brings the newly-active profile's legs up — no setup-key re-prompt,
// no Auth0 OIDC redirect (the netbird-stock GUI failure modes this
// package was built to replace; see goat-trunk
// docs/operations/troubleshooting.md "Netbird GUI app opens
// login.netbird.io instead of self-hosted").
//
// On-disk layout (under ConfigDir):
//
//	profiles/
//	  <slug>.cbor       — bundle bytes, mode 0600 (CPDevicePrivkey is in here)
//	  <slug>.meta.json  — {name, mode, createdAt, updatedAt}, mode 0644
//	active.json         — {active: "<slug>"}
//
// Slugs are derived from the profile name (lowercased, non-alnum
// collapsed to dashes) so on-disk filenames stay shell-friendly while
// the user-visible Name keeps its original case + spacing.
//
// Atomic writes — every mutating method writes through CreateTemp +
// fsync + rename, so a crash mid-write leaves the previous file
// untouched. The active marker is written LAST in setActiveProfile
// so a crash mid-switch keeps the previous active profile pointing
// at a profile we know is reachable.
//
// Clobber resistance — bundle bytes (the cached enrollment creds)
// are immutable from the moment of import. setActiveProfile,
// renameProfile, and setMode never touch the .cbor file. The only
// codepath that overwrites .cbor is addProfile/Replace on the same
// slug, and that's gated behind explicit user action (re-importing
// a bundle).
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// Profile binds a verified bundle to a per-profile Mode + the
// bookkeeping fields the UI surfaces. Held in-memory by Store after
// load; the bundle field is reparsed from disk on each load (it
// carries 1-2 KiB plus the CPDevicePrivkey, so we don't keep it in
// the Store's index — only the lightweight Info).
type Profile struct {
	Name      string
	Mode      mode.Mode
	Bundle    *bundle.EnrollmentBundle
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Info is the lightweight summary the store hands back from List —
// just enough for the tray submenu + Settings pane without paying to
// reparse every bundle. UI calls Load(name) when it needs the full
// Profile (specifically: when the user switches active profile).
type Info struct {
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Mode      mode.Mode `json:"mode"`
	DeviceID  string    `json:"device_id,omitempty"`
	Site      string    `json:"site,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Active    bool      `json:"active"`
}

// meta is the on-disk shape of <slug>.meta.json. Kept separate from
// Info so the disk format doesn't carry derived fields (Active is
// always relative to active.json; DeviceID/Site/ExpiresAt come from
// the bundle).
type meta struct {
	Name      string    `json:"name"`
	Mode      mode.Mode `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// activeMarker is the on-disk shape of active.json.
type activeMarker struct {
	Active string `json:"active"`
}

// Store is the filesystem-backed multi-profile store. Safe for
// concurrent use; mutating methods take the store's write lock and
// fsync to disk before returning.
type Store struct {
	dir        string // <ConfigDir>/profiles
	activePath string // <ConfigDir>/active.json
	trustRoots *bundle.TrustRoots

	mu sync.RWMutex
}

// Config wires the store's filesystem paths and trust set.
type Config struct {
	// Dir is the directory the store reads/writes profile files under.
	// Created on first write (mode 0700 since bundle.cbor files inside
	// carry CPDevicePrivkey).
	Dir string

	// ActivePath is the path to the active.json marker file.
	ActivePath string

	// TrustRoots is the offline-CA pubkey set inbound bundles must
	// verify against on AddProfile. Empty TrustRoots makes AddProfile
	// fail-closed via bundle.ErrNoTrustRoots.
	TrustRoots *bundle.TrustRoots
}

// New constructs a Store. Side-effect-free until the first mutating
// call; List/Load on a missing directory returns an empty list /
// ErrNotFound respectively.
func New(cfg Config) (*Store, error) {
	if cfg.Dir == "" {
		return nil, errors.New("profile: Dir required")
	}
	if cfg.ActivePath == "" {
		return nil, errors.New("profile: ActivePath required")
	}
	return &Store{
		dir:        cfg.Dir,
		activePath: cfg.ActivePath,
		trustRoots: cfg.TrustRoots,
	}, nil
}

// ErrNotFound is returned when a profile slug has no on-disk
// representation. UI code distinguishes "store empty" (List returns
// nil) from "specific profile missing" (Load/Remove returns
// ErrNotFound).
var ErrNotFound = errors.New("profile: not found")

// ErrAlreadyExists is returned when AddProfile would clobber an
// existing slug. RenameProfile uses this when the target slug
// already exists.
var ErrAlreadyExists = errors.New("profile: already exists")

// ErrInvalidName is returned when a candidate name slugifies to
// empty (all-punctuation, all-whitespace, etc.) or hits the
// reserved-name list.
var ErrInvalidName = errors.New("profile: invalid name")

// reservedSlugs is the small set of filename-like strings we refuse
// to use as profile slugs because they collide with the store's own
// marker files or platform conventions.
var reservedSlugs = map[string]struct{}{
	"active": {}, // shares stem with active.json
	"":       {},
	".":      {},
	"..":     {},
}

// slugRegex is the post-lowercase character class. Anything outside
// it collapses to a single dash.
var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify maps a user-visible Name to an on-disk slug. Idempotent
// (Slugify(Slugify(x)) == Slugify(x)) and stable across goroutines
// (no random component). Returns "" for empty input; callers check
// against ErrInvalidName.
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// validateName enforces the name+slug invariants. Centralised so
// AddProfile / RenameProfile share the same error surface.
func validateName(name string) (string, error) {
	slug := Slugify(name)
	if slug == "" {
		return "", fmt.Errorf("%w: %q slugifies to empty", ErrInvalidName, name)
	}
	if _, ok := reservedSlugs[slug]; ok {
		return "", fmt.Errorf("%w: %q is reserved", ErrInvalidName, slug)
	}
	return slug, nil
}

// List returns the lightweight summary for every profile on disk,
// sorted by name. Missing directory is not an error — an
// uninitialised store returns nil, nil. The Active flag is set on
// the profile that matches active.json (if any).
func (s *Store) List() ([]Info, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir: %w", err)
	}
	active, _ := s.readActiveMarker()
	out := make([]Info, 0, len(entries)/2)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".meta.json") {
			continue
		}
		slug := strings.TrimSuffix(name, ".meta.json")
		info, err := s.summarise(slug)
		if err != nil {
			// Skip malformed entries — they shouldn't block the UI
			// from rendering the healthy ones. Operators see them via
			// the structured-log line below.
			continue
		}
		info.Active = (slug == active)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// summarise reads <slug>.meta.json + <slug>.cbor and assembles an
// Info. The CBOR parse is bundle.Unmarshal (no signature check —
// that happened at AddProfile time; we trust on-disk bundles).
//
// Bundle-read / -parse failures intentionally return partial Info +
// nil err: the meta.json fields (Name, Mode, timestamps) are still
// usable for the UI even if the bundle file got truncated or
// removed out-of-band. The lint exemption below documents that the
// nil-err return is deliberate (operator sees the partial row + an
// expiry-blank cell rather than the whole list disappearing).
func (s *Store) summarise(slug string) (Info, error) {
	m, err := s.readMeta(slug)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Name:      m.Name,
		Slug:      slug,
		Mode:      m.Mode,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	data, readErr := os.ReadFile(s.bundlePath(slug))
	if readErr != nil {
		return info, nil //nolint:nilerr // partial info: bundle missing is non-fatal for List
	}
	b, parseErr := bundle.Unmarshal(data)
	if parseErr != nil {
		return info, nil //nolint:nilerr // partial info: bundle malformed is non-fatal for List
	}
	info.DeviceID = b.DeviceID
	info.Site = b.Site
	info.ExpiresAt = b.ExpiresAt
	return info, nil
}

// Load returns the full Profile for the given slug — meta + parsed
// bundle. Used when the daemon needs the bundle bytes to configure
// the tunnel manager (setActiveProfile, Connect after a restart).
func (s *Store) Load(slug string) (*Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadLocked(slug)
}

func (s *Store) loadLocked(slug string) (*Profile, error) {
	m, err := s.readMeta(slug)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.bundlePath(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: bundle missing for slug %q", ErrNotFound, slug)
		}
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	b, err := bundle.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	return &Profile{
		Name:      m.Name,
		Mode:      m.Mode,
		Bundle:    b,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

// AddProfileRequest carries the inputs Add needs. Mode is required
// — caller picks the per-profile mode at creation time (typically
// the daemon's current mode, or whatever the bundle's capabilities
// imply via bundle.HasInnerMesh + HasWgCp0).
type AddProfileRequest struct {
	Name        string
	Mode        mode.Mode
	BundleBytes []byte
	// Replace permits overwriting an existing profile with the same
	// slug. False (the default) makes AddProfile return
	// ErrAlreadyExists on slug collision.
	Replace bool
}

// Add verifies the bundle, slugifies the name, and writes
// <slug>.cbor + <slug>.meta.json atomically. Does NOT touch
// active.json — switching active is a separate call.
//
// Returns the Info that List() would return for the new profile,
// including the resolved Slug and CreatedAt/UpdatedAt.
func (s *Store) Add(req AddProfileRequest) (Info, error) {
	slug, err := validateName(req.Name)
	if err != nil {
		return Info{}, err
	}
	if !req.Mode.Valid() {
		return Info{}, fmt.Errorf("profile: invalid mode %q", req.Mode)
	}
	if len(req.BundleBytes) == 0 {
		return Info{}, errors.New("profile: empty bundle bytes")
	}
	b, err := bundle.Unmarshal(req.BundleBytes)
	if err != nil {
		return Info{}, fmt.Errorf("parse bundle: %w", err)
	}
	if s.trustRoots == nil || s.trustRoots.Empty() {
		return Info{}, bundle.ErrNoTrustRoots
	}
	if err := s.trustRoots.VerifyBundle(b); err != nil {
		return Info{}, fmt.Errorf("verify bundle: %w", err)
	}
	if err := b.CheckCPDeviceKeypair(); err != nil {
		return Info{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !req.Replace {
		if _, err := s.readMeta(slug); err == nil {
			return Info{}, fmt.Errorf("%w: slug %q", ErrAlreadyExists, slug)
		}
	}

	now := time.Now().UTC()
	m := meta{
		Name:      strings.TrimSpace(req.Name),
		Mode:      req.Mode,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Preserve CreatedAt on Replace so the user's original creation
	// time survives a re-import.
	if req.Replace {
		if prev, err := s.readMeta(slug); err == nil {
			m.CreatedAt = prev.CreatedAt
		}
	}

	if err := s.writeBundle(slug, req.BundleBytes); err != nil {
		return Info{}, err
	}
	if err := s.writeMeta(slug, m); err != nil {
		// Best-effort rollback of the bundle write: if the meta write
		// failed we don't want a half-installed profile.
		_ = os.Remove(s.bundlePath(slug))
		return Info{}, err
	}

	info := Info{
		Name:      m.Name,
		Slug:      slug,
		Mode:      m.Mode,
		DeviceID:  b.DeviceID,
		Site:      b.Site,
		ExpiresAt: b.ExpiresAt,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	active, _ := s.readActiveMarker()
	info.Active = (slug == active)
	return info, nil
}

// Remove deletes both files for the given slug. If the slug is the
// active profile, also clears active.json — callers (daemon) treat
// "no active" as "fall back to first remaining profile, or no
// profile" depending on context.
func (s *Store) Remove(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(slug); err != nil {
		return err
	}
	active, _ := s.readActiveMarker()
	if err := os.Remove(s.metaPath(slug)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove meta: %w", err)
	}
	if err := os.Remove(s.bundlePath(slug)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove bundle: %w", err)
	}
	if slug == active {
		if err := s.writeActiveLocked(""); err != nil {
			return fmt.Errorf("clear active: %w", err)
		}
	}
	return nil
}

// Rename moves <oldSlug>.* to the slug derived from newName. The
// new slug must not already exist (returns ErrAlreadyExists).
// active.json is updated atomically if it pointed at oldSlug.
//
// Bundle bytes are NOT modified — only the meta.json + filename
// change. The CPDevicePrivkey + cached enrollment creds survive the
// rename intact.
func (s *Store) Rename(oldSlug, newName string) (Info, error) {
	newSlug, err := validateName(newName)
	if err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if newSlug == oldSlug {
		// Updating only the display name (slug-equivalent rename).
		// Treat as in-place meta edit.
		m, err := s.readMeta(oldSlug)
		if err != nil {
			return Info{}, err
		}
		m.Name = strings.TrimSpace(newName)
		m.UpdatedAt = time.Now().UTC()
		if err := s.writeMeta(oldSlug, m); err != nil {
			return Info{}, err
		}
		return s.summarise(oldSlug)
	}
	if _, err := s.readMeta(newSlug); err == nil {
		return Info{}, fmt.Errorf("%w: slug %q", ErrAlreadyExists, newSlug)
	}
	m, err := s.readMeta(oldSlug)
	if err != nil {
		return Info{}, err
	}
	// Read the bundle bytes into memory so we can write to the new
	// slug atomically and remove the old one after.
	data, err := os.ReadFile(s.bundlePath(oldSlug))
	if err != nil {
		return Info{}, fmt.Errorf("read bundle: %w", err)
	}
	m.Name = strings.TrimSpace(newName)
	m.UpdatedAt = time.Now().UTC()
	if err := s.writeBundle(newSlug, data); err != nil {
		return Info{}, err
	}
	if err := s.writeMeta(newSlug, m); err != nil {
		_ = os.Remove(s.bundlePath(newSlug))
		return Info{}, err
	}
	// Old files go after the new ones are durably in place.
	_ = os.Remove(s.bundlePath(oldSlug))
	_ = os.Remove(s.metaPath(oldSlug))
	active, _ := s.readActiveMarker()
	if active == oldSlug {
		if err := s.writeActiveLocked(newSlug); err != nil {
			return Info{}, fmt.Errorf("update active: %w", err)
		}
	}
	return s.summarise(newSlug)
}

// SetActive writes active.json atomically. The daemon's reconcile
// loop reads this and tears-down/brings-up the legs to match the
// new profile's mode. Returns the previous active slug ("" if none).
func (s *Store) SetActive(slug string) (previous string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug != "" {
		if _, err := s.readMeta(slug); err != nil {
			return "", err
		}
	}
	previous, _ = s.readActiveMarker()
	if err := s.writeActiveLocked(slug); err != nil {
		return previous, err
	}
	return previous, nil
}

// Active returns the slug currently marked active. "" means no
// active profile (a fresh install, or all profiles removed).
func (s *Store) Active() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readActiveMarker()
}

// UpdateMode rewrites <slug>.meta.json with the new Mode, preserving
// every other field. Used when the user picks a different mode for
// the active profile via the Settings → Mode pane (the existing
// setMode IPC call routes here). Does NOT touch the bundle.
func (s *Store) UpdateMode(slug string, m mode.Mode) error {
	if !m.Valid() {
		return fmt.Errorf("profile: invalid mode %q", m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.readMeta(slug)
	if err != nil {
		return err
	}
	cur.Mode = m
	cur.UpdatedAt = time.Now().UTC()
	return s.writeMeta(slug, cur)
}

// --- internal file helpers ---

func (s *Store) bundlePath(slug string) string { return filepath.Join(s.dir, slug+".cbor") }
func (s *Store) metaPath(slug string) string   { return filepath.Join(s.dir, slug+".meta.json") }

func (s *Store) readMeta(slug string) (meta, error) {
	data, err := os.ReadFile(s.metaPath(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return meta{}, fmt.Errorf("%w: meta missing for slug %q", ErrNotFound, slug)
		}
		return meta{}, fmt.Errorf("read meta: %w", err)
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return meta{}, fmt.Errorf("parse meta: %w", err)
	}
	return m, nil
}

func (s *Store) readActiveMarker() (string, error) {
	data, err := os.ReadFile(s.activePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read active: %w", err)
	}
	var a activeMarker
	if err := json.Unmarshal(data, &a); err != nil {
		return "", fmt.Errorf("parse active: %w", err)
	}
	return a.Active, nil
}

func (s *Store) writeMeta(slug string, m meta) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir profiles: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	return writeAtomic(s.metaPath(slug), data, 0o644)
}

func (s *Store) writeBundle(slug string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir profiles: %w", err)
	}
	return writeAtomic(s.bundlePath(slug), data, 0o600)
}

func (s *Store) writeActiveLocked(slug string) error {
	if err := os.MkdirAll(filepath.Dir(s.activePath), 0o700); err != nil {
		return fmt.Errorf("mkdir config: %w", err)
	}
	data, err := json.MarshalIndent(activeMarker{Active: slug}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal active: %w", err)
	}
	return writeAtomic(s.activePath, data, 0o644)
}

// writeAtomic does the temp-fsync-rename dance. Same pattern the
// daemon's importBundle uses for bundle.cbor — keep them aligned so
// a debugger / strace reader sees the same shape.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".profile-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
