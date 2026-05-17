//go:build race

package daemon

// raceDetectorEnabled reports whether the test binary was built with
// `go test -race`. The three-mode smokes that exercise the real
// netbird embed.Client (NetbirdOnly + Combined) skip under -race
// because of the known upstream race between embed.Client.Start's
// engine-state writes and embed.Client.Stop's read on shutdown.
// Same pattern as internal/innermesh/race_{on,off}.go.
const raceDetectorEnabled = true
