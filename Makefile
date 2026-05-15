# goat-client Makefile — convenience targets layered on `go` and the
# repo scripts. Most workflows still use `go build` / `go test` directly;
# this file is the single shorthand for cross-cutting tasks that span
# multiple packages or need a non-default flag set.

.PHONY: smoke-modes

# smoke-modes runs the Block 76N M4 in-process three-mode smoke test:
# wg-cp0-only against a fake tunnel, plus netbird-only and combined
# against the fakemgmt+fakesignal pair from internal/innermesh/fakemgmt.
#
# Goes hand-in-hand with the operator-fired
# scripts/smoke-headless-three-mode.sh (which exercises the install →
# import-bundle → systemctl-active path on a real Linux host) — this
# Makefile target is the hermetic-CI half of that pair.
smoke-modes:
	go test -run TestThreeModeSmoke ./internal/daemon/...
