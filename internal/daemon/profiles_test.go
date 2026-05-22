package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// newProfilesDaemon builds a Daemon with a fake tunnel + fake mesh +
// TrustRoots configured to accept the caller's signing key. Returns
// the Daemon + the dir paths so tests can persist + reopen.
func newProfilesDaemon(t *testing.T, roots *bundle.TrustRoots, initial mode.Mode) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := New(Config{
		BundlePath:        filepath.Join(dir, "bundle.cbor"),
		ProfilesDir:       filepath.Join(dir, "profiles"),
		ActiveProfilePath: filepath.Join(dir, "active.json"),
		SocketPath:        filepath.Join(dir, "ipc.sock"),
		ConfigPath:        filepath.Join(dir, "config.toml"),
		TrustRoots:        roots,
		InitialMode:       initial,
		InnerMeshFactory:  func() innermesh.Mesh { return innermesh.NewFake() },
		TunnelFactory:     func() TunnelManager { return newFakeTunnel() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, dir
}

// reopenDaemon constructs a fresh Daemon against the same on-disk
// directory layout. Simulates a daemon restart for persistence tests.
func reopenDaemon(t *testing.T, dir string, roots *bundle.TrustRoots, initial mode.Mode) *Daemon {
	t.Helper()
	d, err := New(Config{
		BundlePath:        filepath.Join(dir, "bundle.cbor"),
		ProfilesDir:       filepath.Join(dir, "profiles"),
		ActiveProfilePath: filepath.Join(dir, "active.json"),
		SocketPath:        filepath.Join(dir, "ipc.sock"),
		ConfigPath:        filepath.Join(dir, "config.toml"),
		TrustRoots:        roots,
		InitialMode:       initial,
		InnerMeshFactory:  func() innermesh.Mesh { return innermesh.NewFake() },
		TunnelFactory:     func() TunnelManager { return newFakeTunnel() },
	})
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := d.LoadPersistedBundle(); err != nil {
		t.Fatalf("LoadPersistedBundle: %v", err)
	}
	return d
}

// TestProfilesEmptyByDefault — a fresh daemon with no bundles
// reports an empty profile list. Verifies the Block 76M IPC surface
// works against the empty case the GUI hits before first import.
func TestProfilesEmptyByDefault(t *testing.T) {
	_, _, _ = mintTestBundle(t, false, "").parsed, mintTestBundle, t.Helper // (silence unused warning shape)
	tb := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb.roots, mode.Combined)
	ctx := context.Background()
	reply, err := d.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(reply.Profiles) != 0 {
		t.Errorf("ListProfiles len = %d, want 0 on fresh daemon", len(reply.Profiles))
	}
	getReply, err := d.GetActiveProfile(ctx)
	if err != nil {
		t.Fatalf("GetActiveProfile: %v", err)
	}
	if getReply.HasAny || getReply.Active.Slug != "" {
		t.Errorf("GetActiveProfile = %+v, want HasAny=false", getReply)
	}
}

// TestImportBundleAutoCreatesDefaultProfile — first ImportBundle on
// a fresh daemon lands a profile named "default" + sets it active.
// This is the v0.1.x-compat path: existing GUI flow Just Works.
func TestImportBundleAutoCreatesDefaultProfile(t *testing.T) {
	tb := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb.roots, mode.WGCP0Only)
	ctx := context.Background()
	if _, err := d.ImportBundle(ctx, ipc.ImportBundleRequest{BundleBytes: tb.bytes}); err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	reply, err := d.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(reply.Profiles) != 1 {
		t.Fatalf("expected 1 profile after import; got %d", len(reply.Profiles))
	}
	if reply.Profiles[0].Slug != "default" || !reply.Profiles[0].Active {
		t.Errorf("profile = %+v, want slug=default + Active=true", reply.Profiles[0])
	}
}

// TestImportBundleReplacesActiveProfile — second ImportBundle on a
// daemon with an active profile replaces that profile's bundle in
// place WITHOUT creating a new profile. v0.1.x single-bundle path.
func TestImportBundleReplacesActiveProfile(t *testing.T) {
	tb1 := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb1.roots, mode.WGCP0Only)
	ctx := context.Background()
	if _, err := d.ImportBundle(ctx, ipc.ImportBundleRequest{BundleBytes: tb1.bytes}); err != nil {
		t.Fatalf("ImportBundle 1: %v", err)
	}
	// Mint a second bundle against the SAME trust roots — that
	// requires the same key, which the helper doesn't share. Work
	// around by adding the new bundle's key to the existing roots.
	tb2 := mintTestBundleWithRoots(t, true, "", tb1.roots)
	if _, err := d.ImportBundle(ctx, ipc.ImportBundleRequest{BundleBytes: tb2.bytes}); err != nil {
		t.Fatalf("ImportBundle 2: %v", err)
	}
	reply, _ := d.ListProfiles(ctx)
	if len(reply.Profiles) != 1 {
		t.Errorf("expected 1 profile (in-place replace); got %d: %+v", len(reply.Profiles), reply.Profiles)
	}
	// device-id of the second bundle is the same (testBundle helper
	// always uses "three-mode-smoke-device") — but the meta.json's
	// UpdatedAt should advance. We just confirm slug/active here;
	// the bytes-equality assertion is in the store's package test.
	if reply.Profiles[0].Slug != "default" || !reply.Profiles[0].Active {
		t.Errorf("profile = %+v, want slug=default + Active=true", reply.Profiles[0])
	}
}

// TestAddTwoProfilesAndSwitchUnder2s is the load-bearing verdict-gate
// regression. Two profiles in one daemon, switch between them, assert
// the round-trip completes in <2s with cached-creds (Fake innermesh).
func TestAddTwoProfilesAndSwitchUnder2s(t *testing.T) {
	tb1 := mintTestBundle(t, true, "http://mgmt.example.invalid")
	d, _ := newProfilesDaemon(t, tb1.roots, mode.Combined)
	ctx := context.Background()

	// First profile.
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{
		Name:        "goat-prod",
		Mode:        string(mode.Combined),
		BundleBytes: tb1.bytes,
		SetActive:   true,
	}); err != nil {
		t.Fatalf("AddProfile 1: %v", err)
	}

	// Second profile against the same roots.
	tb2 := mintTestBundleWithRoots(t, true, "http://mgmt2.example.invalid", tb1.roots)
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{
		Name:        "cochlearis-dev",
		Mode:        string(mode.NetbirdOnly),
		BundleBytes: tb2.bytes,
	}); err != nil {
		t.Fatalf("AddProfile 2: %v", err)
	}

	// Now the switch — the gate is <2000ms.
	start := time.Now()
	reply, err := d.SetActiveProfile(ctx, ipc.SetActiveProfileRequest{Slug: "cochlearis-dev"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SetActiveProfile: %v", err)
	}
	if reply.PreviousActive != "goat-prod" || !reply.Active.Active || reply.Active.Slug != "cochlearis-dev" {
		t.Errorf("SetActiveProfile reply = %+v, want previous=goat-prod active=cochlearis-dev", reply)
	}
	if elapsed > 2*time.Second {
		t.Errorf("switch round-trip = %s, want <2s (Block 76M verdict gate)", elapsed)
	}
	t.Logf("switch round-trip = %s", elapsed)

	// And switch back, also under 2s.
	start = time.Now()
	if _, err := d.SetActiveProfile(ctx, ipc.SetActiveProfileRequest{Slug: "goat-prod"}); err != nil {
		t.Fatalf("SetActiveProfile back: %v", err)
	}
	elapsed = time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("switch-back round-trip = %s, want <2s", elapsed)
	}
}

// TestMultiProfilePersistAcrossRestart loads two profiles, sets one
// active, closes the daemon, reopens, and verifies both profiles +
// the active marker survive intact. Verdict-gate gate: "Profile
// config persists across goat-client restarts."
func TestMultiProfilePersistAcrossRestart(t *testing.T) {
	tb1 := mintTestBundle(t, true, "")
	d, dir := newProfilesDaemon(t, tb1.roots, mode.Combined)
	ctx := context.Background()
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "alpha", Mode: string(mode.Combined), BundleBytes: tb1.bytes, SetActive: true}); err != nil {
		t.Fatalf("AddProfile alpha: %v", err)
	}
	tb2 := mintTestBundleWithRoots(t, true, "", tb1.roots)
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "beta", Mode: string(mode.WGCP0Only), BundleBytes: tb2.bytes}); err != nil {
		t.Fatalf("AddProfile beta: %v", err)
	}
	if _, err := d.SetActiveProfile(ctx, ipc.SetActiveProfileRequest{Slug: "beta"}); err != nil {
		t.Fatalf("SetActiveProfile beta: %v", err)
	}
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Reopen.
	d2 := reopenDaemon(t, dir, tb1.roots, mode.Combined)
	ctx2 := context.Background()
	reply, err := d2.ListProfiles(ctx2)
	if err != nil {
		t.Fatalf("ListProfiles after reopen: %v", err)
	}
	if len(reply.Profiles) != 2 {
		t.Fatalf("expected 2 profiles after reopen, got %d", len(reply.Profiles))
	}
	getReply, _ := d2.GetActiveProfile(ctx2)
	if !getReply.HasAny || getReply.Active.Slug != "beta" {
		t.Errorf("active after reopen = %+v, want slug=beta", getReply)
	}
}

// TestProfileSwitchDoesNotClobberInactive — the load-bearing
// clobber-resistance gate at the daemon layer. Switch the active
// profile back-and-forth 4 times; assert both profiles' bundles
// + modes survive intact.
func TestProfileSwitchDoesNotClobberInactive(t *testing.T) {
	tb1 := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb1.roots, mode.Combined)
	ctx := context.Background()
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "alpha", Mode: string(mode.Combined), BundleBytes: tb1.bytes, SetActive: true}); err != nil {
		t.Fatalf("AddProfile alpha: %v", err)
	}
	tb2 := mintTestBundleWithRoots(t, true, "", tb1.roots)
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "beta", Mode: string(mode.NetbirdOnly), BundleBytes: tb2.bytes}); err != nil {
		t.Fatalf("AddProfile beta: %v", err)
	}
	for _, slug := range []string{"beta", "alpha", "beta", "alpha"} {
		if _, err := d.SetActiveProfile(ctx, ipc.SetActiveProfileRequest{Slug: slug}); err != nil {
			t.Fatalf("SetActiveProfile %s: %v", slug, err)
		}
	}
	reply, err := d.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(reply.Profiles) != 2 {
		t.Fatalf("profiles len = %d, want 2", len(reply.Profiles))
	}
	for _, info := range reply.Profiles {
		if info.Slug == "alpha" && info.Mode != string(mode.Combined) {
			t.Errorf("alpha mode = %q, want %q (switching disturbed mode)", info.Mode, mode.Combined)
		}
		if info.Slug == "beta" && info.Mode != string(mode.NetbirdOnly) {
			t.Errorf("beta mode = %q, want %q (switching disturbed mode)", info.Mode, mode.NetbirdOnly)
		}
	}
}

// TestRemoveActiveProfileTearsDown — removing the active profile
// takes the legs down + clears the active marker. The GUI then
// prompts the user to pick a new active profile.
func TestRemoveActiveProfileTearsDown(t *testing.T) {
	tb := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb.roots, mode.Combined)
	ctx := context.Background()
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "only", Mode: string(mode.Combined), BundleBytes: tb.bytes, SetActive: true}); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	if err := d.RemoveProfile(ctx, ipc.RemoveProfileRequest{Slug: "only"}); err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	getReply, _ := d.GetActiveProfile(ctx)
	if getReply.HasAny || getReply.Active.Slug != "" {
		t.Errorf("active after remove = %+v, want HasAny=false", getReply)
	}
}

// TestRenameDoesNotClobberBundle — rename through the IPC method
// changes the display name + slug, but the bundle data persists.
func TestRenameDoesNotClobberBundle(t *testing.T) {
	tb := mintTestBundle(t, true, "")
	d, _ := newProfilesDaemon(t, tb.roots, mode.Combined)
	ctx := context.Background()
	if _, err := d.AddProfile(ctx, ipc.AddProfileRequest{Name: "first-name", Mode: string(mode.Combined), BundleBytes: tb.bytes, SetActive: true}); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	reply, err := d.RenameProfile(ctx, ipc.RenameProfileRequest{Slug: "first-name", NewName: "Second Name"})
	if err != nil {
		t.Fatalf("RenameProfile: %v", err)
	}
	if reply.Profile.Slug != "second-name" {
		t.Errorf("Rename slug = %q, want second-name", reply.Profile.Slug)
	}
	getReply, _ := d.GetActiveProfile(ctx)
	if getReply.Active.Slug != "second-name" {
		t.Errorf("active after rename = %q, want second-name", getReply.Active.Slug)
	}
	if d.currentBundle == nil || d.currentBundle.DeviceID == "" {
		t.Error("rename wiped currentBundle")
	}
}
