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
// (no real TOML library — a handful of scalar keys). Keeping it minimal
// lets packagers drop lines into /etc/goat-client/config.toml without
// dragging a parser in.
type PersistedConfig struct {
	// Mode is the v0.2 selector. Empty value means "use Default".
	Mode Mode

	// MeshDNSServers / MeshDNSSearchDomains / MeshDNSMatchDomains are
	// the operator-set host-DNS values for the wg-cp0 mesh zones,
	// comma-separated on disk:
	//
	//   mesh_dns_servers = "100.64.165.203"
	//   mesh_dns_search_domains = "efdi.netbird.efdi-backbone.net"
	//   mesh_dns_match_domains = "netbird.efdi-backbone.net"
	//
	// They fill tunnel.Config's DNS fields when the bundle leaves them
	// empty (the bundle schema does not carry nameservers yet — this is
	// the out-of-band path tunnel.Config.DNSServers documents). With
	// names fronting on, these are also what the forwarder forwards to
	// (live-first) while the OS points at the forwarder.
	MeshDNSServers       []string
	MeshDNSSearchDomains []string
	MeshDNSMatchDomains  []string
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
	var body strings.Builder
	fmt.Fprintf(&body, "# goat-client config (v0.2). Managed by `goat-client setmode` or the installer.\nmode = %q\n", cfg.Mode)
	// Preserve operator-set mesh-DNS keys — a setmode round-trip must
	// never drop them.
	if len(cfg.MeshDNSServers) > 0 {
		fmt.Fprintf(&body, "mesh_dns_servers = %q\n", strings.Join(cfg.MeshDNSServers, ","))
	}
	if len(cfg.MeshDNSSearchDomains) > 0 {
		fmt.Fprintf(&body, "mesh_dns_search_domains = %q\n", strings.Join(cfg.MeshDNSSearchDomains, ","))
	}
	if len(cfg.MeshDNSMatchDomains) > 0 {
		fmt.Fprintf(&body, "mesh_dns_match_domains = %q\n", strings.Join(cfg.MeshDNSMatchDomains, ","))
	}
	if _, err := tmp.WriteString(body.String()); err != nil {
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

// parse is a minimal key = "value" extractor; intentionally does not
// pull in a TOML library for a handful of scalar keys.
func parse(r io.Reader) (PersistedConfig, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return PersistedConfig{}, fmt.Errorf("read: %w", err)
	}
	var cfg PersistedConfig
	for _, raw := range strings.Split(string(buf), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.Trim(strings.TrimSpace(v), "\"'")
		switch key {
		case "mode":
			if val == "" {
				continue
			}
			m, perr := Parse(val)
			if perr != nil {
				return PersistedConfig{}, fmt.Errorf("parse mode %q: %w", val, perr)
			}
			cfg.Mode = m
		case "mesh_dns_servers":
			cfg.MeshDNSServers = splitCSV(val)
		case "mesh_dns_search_domains":
			cfg.MeshDNSSearchDomains = splitCSV(val)
		case "mesh_dns_match_domains":
			cfg.MeshDNSMatchDomains = splitCSV(val)
		}
	}
	return cfg, nil
}

// splitCSV splits a comma-separated value list, trimming whitespace and
// dropping empties. Empty input yields nil.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
