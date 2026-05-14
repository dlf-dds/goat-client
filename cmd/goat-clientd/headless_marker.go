//go:build headless

package main

// This file is only compiled under the `headless` build tag. Its
// presence asserts (via compilation) that the goat-clientd binary
// being built has no fyne.io/... import in its transitive dependency
// graph. If a future change accidentally pulls Fyne into a package
// goat-clientd imports, `go build -tags headless ./cmd/goat-clientd`
// will fail because Fyne's runtime needs CGO + OpenGL, which the
// headless build pipeline does not provide. The CI matrix for the
// goat-client-headless package always builds with `-tags headless`.
//
// The const below is unused at runtime; it just makes the headless
// build tag observable in `go list` / `go env` output.
const headlessBuild = true
