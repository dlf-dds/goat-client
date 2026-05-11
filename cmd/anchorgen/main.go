// Command anchorgen reads a YAML descriptor of pinned offline-CA root
// public keys and emits a Go source file declaring the embedded set
// consumed by package internal/trustanchor.
//
// Operator workflow:
//
//	# 1. Edit internal/trustanchor/anchors.yaml — add the new anchor,
//	#    bump valid_from/valid_until, leave the outgoing anchor in place
//	#    until its rotation window closes.
//	# 2. Regenerate the embedded source:
//	go generate ./internal/trustanchor
//	# 3. Verify deterministic output, run tests, commit both files.
//	go test ./internal/trustanchor/...
//
// Output is byte-for-byte deterministic given the same input — the tool
// sorts anchors by name, formats integers in a fixed shape, and emits
// no timestamps or hostnames. Re-running anchorgen against an unchanged
// YAML produces an identical embedded.go (verified by the
// TestAnchorgenDeterministic test in internal/trustanchor).
//
// Block 79 cutover: anchors are now ECDSA P-256. The generator extracts
// the SubjectPublicKeyInfo (DER) from the YAML's PEM block — whether
// it's a raw `PUBLIC KEY` block or an X.509 `CERTIFICATE` block — and
// emits the SPKI bytes verbatim into embedded.go. NewSet parses the
// SPKI back into *ecdsa.PublicKey at package-init time. The detour
// through SPKI keeps the generated source readable (one byte slice per
// anchor) and avoids reconstructing X/Y *big.Int literals in code.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type yamlDoc struct {
	Anchors []yamlAnchor `yaml:"anchors"`
}

type yamlAnchor struct {
	Name         string `yaml:"name"`
	Issuer       string `yaml:"issuer"`
	ValidFrom    string `yaml:"valid_from"`
	ValidUntil   string `yaml:"valid_until"`
	PublicKeyPEM string `yaml:"public_key_pem"`
}

type anchor struct {
	name       string
	issuer     string
	validFrom  time.Time
	validUntil time.Time
	spki       []byte
}

func main() {
	in := flag.String("in", "anchors.yaml", "path to YAML anchor descriptor")
	out := flag.String("out", "embedded.go", "path to Go source to emit")
	pkg := flag.String("package", "trustanchor", "package name for emitted file")
	flag.Parse()

	if err := run(*in, *out, *pkg); err != nil {
		log.Fatalf("anchorgen: %v", err)
	}
}

func run(inPath, outPath, pkgName string) error {
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}
	var doc yamlDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	if len(doc.Anchors) == 0 {
		return fmt.Errorf("no anchors in %s", inPath)
	}
	anchors, err := decode(doc.Anchors)
	if err != nil {
		return err
	}
	src, err := emit(pkgName, anchors)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, src, 0o644) //nolint:gosec // generated Go file with public-key constants — world-readable is correct
}

func decode(in []yamlAnchor) ([]anchor, error) {
	seen := map[string]bool{}
	out := make([]anchor, 0, len(in))
	for i, a := range in {
		if a.Name == "" {
			return nil, fmt.Errorf("anchor %d: name is empty", i)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("anchor %q: duplicate name", a.Name)
		}
		seen[a.Name] = true
		if a.Issuer == "" {
			return nil, fmt.Errorf("anchor %q: issuer is empty", a.Name)
		}
		from, err := time.Parse(time.RFC3339, a.ValidFrom)
		if err != nil {
			return nil, fmt.Errorf("anchor %q: valid_from: %w", a.Name, err)
		}
		until, err := time.Parse(time.RFC3339, a.ValidUntil)
		if err != nil {
			return nil, fmt.Errorf("anchor %q: valid_until: %w", a.Name, err)
		}
		if !until.After(from) {
			return nil, fmt.Errorf("anchor %q: valid_until must be strictly after valid_from", a.Name)
		}
		spki, err := decodePEM(a.PublicKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("anchor %q: %w", a.Name, err)
		}
		out = append(out, anchor{
			name:       a.Name,
			issuer:     a.Issuer,
			validFrom:  from.UTC(),
			validUntil: until.UTC(),
			spki:       spki,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// decodePEM extracts the SubjectPublicKeyInfo (DER) from the YAML's
// public_key_pem field. Accepts both `PUBLIC KEY` (raw SPKI) and
// `CERTIFICATE` (X.509 wrapping the same key) block types — mirrors
// goat-trunk's wg-cp0-bundle-apply loadCAPubkey (commit dc3944fe). The
// curve assertion runs at this stage so a bad anchor fails generation
// rather than runtime.
func decodePEM(s string) ([]byte, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	var ec *ecdsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKIX key: %w", err)
		}
		var ok bool
		ec, ok = pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key is %T, want *ecdsa.PublicKey", pub)
		}
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		var ok bool
		ec, ok = cert.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("certificate carries %T, want *ecdsa.PublicKey", cert.PublicKey)
		}
	default:
		return nil, fmt.Errorf("unexpected PEM block type %q (want PUBLIC KEY or CERTIFICATE)", block.Type)
	}
	if ec.Curve == nil || ec.Curve.Params().Name != elliptic.P256().Params().Name {
		got := "nil"
		if ec.Curve != nil {
			got = ec.Curve.Params().Name
		}
		return nil, fmt.Errorf("ECDSA pubkey curve is %s, want P-256", got)
	}
	// Re-marshal so the embedded SPKI is canonical even if the YAML
	// carried a syntactically valid but non-canonical encoding —
	// keeps anchorgen output deterministic across operator workstations.
	canon, err := x509.MarshalPKIXPublicKey(ec)
	if err != nil {
		return nil, fmt.Errorf("re-marshal PKIX: %w", err)
	}
	return canon, nil
}

func emit(pkgName string, anchors []anchor) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by cmd/anchorgen. DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Source: anchors.yaml — re-run `go generate ./internal/trustanchor`\n")
	fmt.Fprintf(&b, "// to regenerate after editing the YAML descriptor.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkgName)
	fmt.Fprintf(&b, "import \"time\"\n\n")
	fmt.Fprintf(&b, "// embedded carries the SubjectPublicKeyInfo (DER) for each pinned\n")
	fmt.Fprintf(&b, "// anchor; NewSet inflates SPKI into *ecdsa.PublicKey at package-init time.\n")
	fmt.Fprintf(&b, "var embedded = []Anchor{\n")
	for _, a := range anchors {
		fmt.Fprintf(&b, "\t{\n")
		fmt.Fprintf(&b, "\t\tName:       %q,\n", a.name)
		fmt.Fprintf(&b, "\t\tIssuer:     %q,\n", a.issuer)
		fmt.Fprintf(&b, "\t\tSPKI:       []byte{%s},\n", formatBytes(a.spki))
		fmt.Fprintf(&b, "\t\tValidFrom:  %s,\n", formatTime(a.validFrom))
		fmt.Fprintf(&b, "\t\tValidUntil: %s,\n", formatTime(a.validUntil))
		fmt.Fprintf(&b, "\t},\n")
	}
	fmt.Fprintf(&b, "}\n")
	formatted, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt emitted source: %w\n--- source ---\n%s", err, b.String())
	}
	return formatted, nil
}

func formatBytes(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("0x%02x", x)
	}
	return strings.Join(parts, ", ")
}

func formatTime(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("time.Date(%d, %d, %d, %d, %d, %d, %d, time.UTC)",
		t.Year(), int(t.Month()), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond())
}
