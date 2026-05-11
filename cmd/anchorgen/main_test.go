package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// synthesizeYAML writes a temp anchors.yaml carrying n freshly-generated
// ECDSA P-256 keys and returns the file path. Each anchor carries a non-
// trivial validity window so dates make it through the round-trip.
func synthesizeYAML(t *testing.T, dir string, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("anchors:\n")
	for i := 0; i < n; i++ {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			t.Fatalf("MarshalPKIXPublicKey: %v", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
		// Indent the PEM so it nests under the YAML literal-block scalar.
		indented := "      " + strings.ReplaceAll(strings.TrimRight(string(pemBytes), "\n"), "\n", "\n      ")
		validFrom := time.Date(2026, time.Month(1+i), 1, 0, 0, 0, 0, time.UTC)
		validUntil := validFrom.Add(365 * 24 * time.Hour)
		b.WriteString("  - name: ca-")
		b.WriteString(strings.ToLower(string(rune('a' + i))))
		b.WriteString("\n")
		b.WriteString("    issuer: ca-")
		b.WriteString(strings.ToLower(string(rune('a' + i))))
		b.WriteString("\n    valid_from: ")
		b.WriteString(validFrom.Format(time.RFC3339))
		b.WriteString("\n    valid_until: ")
		b.WriteString(validUntil.Format(time.RFC3339))
		b.WriteString("\n    public_key_pem: |\n")
		b.WriteString(indented)
		b.WriteString("\n")
	}
	path := filepath.Join(dir, "anchors.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func TestRunDeterministic(t *testing.T) {
	// Generating the same YAML twice must produce byte-for-byte
	// identical Go source. Without this, the embedded.go diff in
	// every PR would be noise; with it, an unexpected diff is a
	// real signal that the YAML changed.
	dir := t.TempDir()
	in := synthesizeYAML(t, dir, 3)
	out1 := filepath.Join(dir, "embedded1.go")
	out2 := filepath.Join(dir, "embedded2.go")
	if err := run(in, out1, "trustanchor"); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	if err := run(in, out2, "trustanchor"); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	a, err := os.ReadFile(out1)
	if err != nil {
		t.Fatalf("read out1: %v", err)
	}
	b, err := os.ReadFile(out2)
	if err != nil {
		t.Fatalf("read out2: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("anchorgen output differs across runs:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", a, b)
	}
}

func TestRunSortsAnchorsByName(t *testing.T) {
	// Operators may list anchors in any order in the YAML —
	// e.g. new entries appended at the bottom. The generator
	// should normalize to sorted-by-name so PR diffs stay localized
	// and Verify's deterministic iteration order is independent of
	// YAML edit history.
	dir := t.TempDir()
	yaml := `anchors:
  - name: ca-z
    issuer: ca-z
    valid_from: 2026-01-01T00:00:00Z
    valid_until: 2027-01-01T00:00:00Z
    public_key_pem: |
` + indentPEM(t, mustGenPEM(t)) + `
  - name: ca-a
    issuer: ca-a
    valid_from: 2026-01-01T00:00:00Z
    valid_until: 2027-01-01T00:00:00Z
    public_key_pem: |
` + indentPEM(t, mustGenPEM(t)) + `
`
	in := filepath.Join(dir, "anchors.yaml")
	if err := os.WriteFile(in, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	out := filepath.Join(dir, "embedded.go")
	if err := run(in, out, "trustanchor"); err != nil {
		t.Fatalf("run: %v", err)
	}
	src, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	idxA := strings.Index(string(src), `"ca-a"`)
	idxZ := strings.Index(string(src), `"ca-z"`)
	if idxA < 0 || idxZ < 0 {
		t.Fatalf("expected both ca-a and ca-z in output, got:\n%s", src)
	}
	if idxA > idxZ {
		t.Errorf("ca-a appears at offset %d, ca-z at %d — want ca-a first (sorted)", idxA, idxZ)
	}
}

func TestRunRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	pemStr := mustGenPEM(t)
	yaml := `anchors:
  - name: dup
    issuer: dup
    valid_from: 2026-01-01T00:00:00Z
    valid_until: 2027-01-01T00:00:00Z
    public_key_pem: |
` + indentPEM(t, pemStr) + `
  - name: dup
    issuer: dup
    valid_from: 2026-01-01T00:00:00Z
    valid_until: 2027-01-01T00:00:00Z
    public_key_pem: |
` + indentPEM(t, pemStr) + `
`
	in := filepath.Join(dir, "anchors.yaml")
	if err := os.WriteFile(in, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	out := filepath.Join(dir, "embedded.go")
	err := run(in, out, "trustanchor")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("run err = %v, want duplicate-name error", err)
	}
}

func mustGenPEM(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return strings.TrimRight(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), "\n")
}

func indentPEM(t *testing.T, s string) string {
	t.Helper()
	return "      " + strings.ReplaceAll(s, "\n", "\n      ")
}
