package mode

import (
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want Mode
	}{
		{"wg-cp0-only", WGCP0Only},
		{"WG-CP0-ONLY", WGCP0Only},
		{"outer", WGCP0Only},
		{"netbird-only", NetbirdOnly},
		{"netbird", NetbirdOnly},
		{"inner", NetbirdOnly},
		{"combined", Combined},
		{"both", Combined},
		{" all\n", Combined},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	if _, err := Parse("nope"); err == nil {
		t.Error("expected error for unknown mode")
	}
}

func TestModeIncludes(t *testing.T) {
	t.Parallel()
	if !WGCP0Only.IncludesWGCP0() || WGCP0Only.IncludesNetbird() {
		t.Errorf("WGCP0Only includes wrong")
	}
	if NetbirdOnly.IncludesWGCP0() || !NetbirdOnly.IncludesNetbird() {
		t.Errorf("NetbirdOnly includes wrong")
	}
	if !Combined.IncludesWGCP0() || !Combined.IncludesNetbird() {
		t.Errorf("Combined includes wrong")
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Missing file → empty config, no error.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if cfg.Mode != "" {
		t.Errorf("missing file should yield empty mode, got %q", cfg.Mode)
	}

	// Save → Load round-trips.
	for _, m := range All() {
		if err := Save(path, PersistedConfig{Mode: m}); err != nil {
			t.Fatalf("Save(%s): %v", m, err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Mode != m {
			t.Errorf("round-trip: got %q want %q", cfg.Mode, m)
		}
	}
}

func TestLoadOrDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	got, err := LoadOrDefault(path)
	if err != nil {
		t.Fatalf("LoadOrDefault: %v", err)
	}
	if got != Default {
		t.Errorf("missing file: got %q want default %q", got, Default)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Save(path, PersistedConfig{Mode: "bogus"}); err == nil {
		t.Error("expected error saving bogus mode")
	}
}
