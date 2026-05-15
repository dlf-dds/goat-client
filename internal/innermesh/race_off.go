//go:build !race

package innermesh

// raceDetectorEnabled reports whether the test binary was built with
// `go test -race`. Used by tests that need to skip under -race when a
// known upstream race (in vendored netbird embed.Client) would
// otherwise trip the detector.
const raceDetectorEnabled = false
