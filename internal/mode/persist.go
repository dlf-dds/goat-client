package mode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PersistedConfig is the on-disk shape — a tiny TOML-ish key=value file
// (no real TOML library, since we only carry one field today). Keeping
// it minimal lets packagers drop a single line into /etc/goat-client/
// config.toml without dragging a parser in.
type PersistedConfig struct {
	// Mode is the v0.2 selector. Empty value means "use Default".
	Mode Mode
}

// DefaultConfigPath returns the platform-conventional config-file path
// the daemon reads on start-up.
//
//   - Linux:   /etc/goat-client/config.toml
//   - macOS:   /Library/Application Support/goat-client/config.toml
//   - Windows: %ProgramData%\goat-client\config.toml
//
// Per-user dev overrides land in $HOME/.config/goat-client/config.toml on
// Linux + macOS or %APPDATA%\goat-client\config.toml on Windows; the
// daemon may read either, but the *system* path is the canonical one
// for the packaged install.
func DefaultConfigPath() string {
	switch runtime.GOOS {
	case "linux":
		return "/etc/goat-client/config.toml"
	case "darwin":
		return "/Library/Application Support/goat-client/config.toml"
	case "windows":
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "goat-client", "config.toml")
		}
		return `C:\ProgramData\goat-client\config.toml`
	}
	return "/etc/goat-client/config.toml"
}

// Load reads path and parses the persisted mode. Missing file is not an
// error — caller falls back to Default. A malformed file is an error
// the caller surfaces to operators.
func Load(path string) (PersistedConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PersistedConfig{}, nil
		}
		return PersistedConfig{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return parse(f)
}

// Save atomically writes a config file. Creates the directory if needed
// (mode 0755 on the dir, 0644 on the file — operators read these via
// `cat`, so they aren't secrets).
func Save(path string, cfg PersistedConfig) error {
	if cfg.Mode != "" && !cfg.Mode.Valid() {
		return fmt.Errorf("save: invalid mode %q", cfg.Mode)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "# goat-client config (v0.2). Managed by `goat-client setmode` or the installer.\nmode = %q\n", cfg.Mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// LoadOrDefault returns the persisted mode, or Default if the file is
// missing or carries an empty mode field. Malformed files surface as
// errors.
func LoadOrDefault(path string) (Mode, error) {
	cfg, err := Load(path)
	if err != nil {
		return "", err
	}
	if cfg.Mode == "" {
		return Default, nil
	}
	if !cfg.Mode.Valid() {
		return "", fmt.Errorf("persisted mode %q is not a known mode", cfg.Mode)
	}
	return cfg.Mode, nil
}

// parse is a minimal `mode = "..."` extractor; intentionally does not
// pull in a TOML library for one field.
func parse(r io.Reader) (PersistedConfig, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return PersistedConfig{}, fmt.Errorf("read: %w", err)
	}
	for _, raw := range strings.Split(string(buf), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) != "mode" {
			continue
		}
		val := strings.TrimSpace(v)
		val = strings.Trim(val, "\"'")
		if val == "" {
			return PersistedConfig{}, nil
		}
		m, perr := Parse(val)
		if perr != nil {
			return PersistedConfig{}, fmt.Errorf("parse mode %q: %w", val, perr)
		}
		return PersistedConfig{Mode: m}, nil
	}
	return PersistedConfig{}, nil
}
