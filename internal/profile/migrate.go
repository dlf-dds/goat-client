package profile

import (
	"errors"
	"fmt"
	"os"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// MigrateLegacyBundle imports a v0.1.x single-bundle.cbor file as a
// "default" profile, if and only if:
//   - the profile store currently has zero profiles (List returns nil),
//     AND
//   - legacyPath exists and parses as a valid signed bundle.
//
// The legacy file is left in place on success — non-destructive
// migration so a downgrade can still find it. Subsequent imports on
// the new daemon go through Store.Add and never touch bundlePath
// again.
//
// Returns the resulting slug (always "default" when migration ran)
// and ok=true. Returns ok=false with no error when migration was
// not needed (store non-empty, or legacy file absent). Surfaces
// real errors only when something unexpected blocks the migration
// (bundle bytes present but malformed, trust-roots reject, etc.) —
// the daemon logs them and proceeds as if no legacy bundle
// existed, so a corrupted legacy file never blocks the daemon
// start-up.
//
// initialMode is the per-profile mode the migration writes into
// the new default profile's meta.json. The daemon passes its
// resolved start-up mode here so the migrated profile preserves
// whatever was in /etc/goat-client/config.toml.
func (s *Store) MigrateLegacyBundle(legacyPath string, initialMode mode.Mode) (slug string, ok bool, err error) {
	existing, listErr := s.List()
	if listErr != nil {
		return "", false, fmt.Errorf("list profiles: %w", listErr)
	}
	if len(existing) > 0 {
		return "", false, nil
	}
	data, readErr := os.ReadFile(legacyPath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read legacy bundle: %w", readErr)
	}
	// Probe-parse so a corrupted legacy file produces an error the
	// caller logs rather than getting wrapped into the Add path.
	if _, parseErr := bundle.Unmarshal(data); parseErr != nil {
		return "", false, fmt.Errorf("parse legacy bundle: %w", parseErr)
	}
	if !initialMode.Valid() {
		initialMode = mode.Default
	}
	info, addErr := s.Add(AddProfileRequest{
		Name:        "default",
		Mode:        initialMode,
		BundleBytes: data,
		Replace:     false,
	})
	if addErr != nil {
		return "", false, fmt.Errorf("add migrated profile: %w", addErr)
	}
	if _, setErr := s.SetActive(info.Slug); setErr != nil {
		return info.Slug, true, fmt.Errorf("set active after migrate: %w", setErr)
	}
	return info.Slug, true, nil
}
