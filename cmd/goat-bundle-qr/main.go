// Package main is goat-bundle-qr, the operator-side tool that wraps a
// CBOR-encoded offline-CA enrollment bundle in a QR code (and emits the
// raw base45 string for paste-in-channel delivery).
//
// Operator workflow: this runs on the same air-gapped workstation as
// goat-bundle-create (Track A). After bundle-create produces bundle.cbor,
// the operator runs:
//
//	goat-bundle-qr -in bundle.cbor -out bundle.png
//
// The PNG can be embedded in an enrollment letter or shown on a kiosk
// screen for the end user to scan with the goat-client mobile app. The
// base45 string printed to stdout can be pasted into a chat channel as an
// alternative delivery path; the desktop bundle-import dialog accepts it
// directly.
//
// The QR is just transport. Authentication is provided by the ECDSA P-256
// signature inside the CBOR bundle, verified downstream against the
// pinned offline-CA root by internal/bundle.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/dlf-dds/goat-client/internal/qrbundle"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("goat-bundle-qr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		inPath  = fs.String("in", "", "path to bundle.cbor (required)")
		outPath = fs.String("out", "", "path to write PNG QR image (optional; if empty only the base45 string is printed)")
		size    = fs.Int("size", 512, "PNG image size in pixels (square)")
		quiet   = fs.Bool("quiet", false, "do not print the base45 string to stdout")
	)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: goat-bundle-qr -in bundle.cbor [-out bundle.png] [-size 512]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" {
		fs.Usage()
		return errors.New("missing required -in")
	}

	raw, err := os.ReadFile(*inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", *inPath, err)
	}

	payload, err := qrbundle.Encode(raw)
	if err != nil {
		return fmt.Errorf("encode bundle: %w", err)
	}

	if !*quiet {
		if _, err := fmt.Fprintln(stdout, payload); err != nil {
			return err
		}
	}

	if *outPath != "" {
		// ECC level Low matches the QR sizing budget assumed in
		// docs/qr-bundle.md: bundles are short-lived and authenticated
		// inside (ECDSA P-256 over the CBOR), so we do not need the QR's
		// own ECC to recover from heavy damage; we want max payload
		// headroom instead.
		qr, err := qrcode.New(payload, qrcode.Low)
		if err != nil {
			return fmt.Errorf("build QR: %w", err)
		}
		if err := qr.WriteFile(*size, *outPath); err != nil {
			return fmt.Errorf("write PNG %s: %w", *outPath, err)
		}
		fmt.Fprintf(stderr, "wrote QR PNG: %s (%d bytes bundle, %d base45 chars)\n",
			*outPath, len(raw), len(payload))
	}
	return nil
}
