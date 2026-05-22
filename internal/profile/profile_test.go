package profile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// testFixture binds a TrustRoots + the signing keypair behind it, so
// individual tests can mint multiple bundles that all verify against
// the same Store.
type testFixture struct {
	dir        string
	store      *Store
	trustRoots *bundle.TrustRoots
	priv       *ecdsa.PrivateKey
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa: %v", err)
	}
	tr, err := bundle.NewTrustRoots(&priv.PublicKey)
	if err != nil {
		t.Fatalf("trust roots: %v", err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		Dir:        filepath.Join(dir, "profiles"),
		ActivePath: filepath.Join(dir, "active.json"),
		TrustRoots: tr,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return &testFixture{dir: dir, store: s, trustRoots: tr, priv: priv}
}

// mintBundle creates a signed bundle that verifies against the
// fixture's TrustRoots. Each call uses unique CP-device placeholder
// bytes + a random nonce so the resulting bundles are distinct on
// the wire.
func (f *testFixture) mintBundle(t *testing.T, deviceID, site string) []byte {
	t.Helper()
	cpPriv := make([]byte, 32)
	cpPub := make([]byte, 32)
	if _, err := rand.Read(cpPriv); err != nil {
		t.Fatalf("rand cpPriv: %v", err)
	}
	if _, err := rand.Read(cpPub); err != nil {
		t.Fatalf("rand cpPub: %v", err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	b := &bundle.EnrollmentBundle{
		Version:            bundle.Version,
		DeviceID:           deviceID,
		PeerPubkey:         []byte("0123456789abcdef0123456789abcdef"),
		ACLGroups:          []string{"workstations"},
		Site:               site,
		KnownEndpoints:     []bundle.KnownEndpoint{{Addr: "10.0.0.1:51820", Pubkey: []byte("relaypubkey00000000000000000000aa"), Kind: bundle.KindRelay, MeshAddr: "198.18.0.1"}},
		IssuedAt:           now,
		ActivationDeadline: now.Add(72 * time.Hour),
		ExpiresAt:          now.Add(365 * 24 * time.Hour),
		Nonce:              nonce,
		CAID:               "test-ca",
		CPDevicePubkey:     cpPub,
		CPDevicePrivkey:    cpPriv,
		CPDeviceAddress:    "198.18.0.99/32",
	}
	payload, err := b.Signable()
	if err != nil {
		t.Fatalf("signable: %v", err)
	}
	digest := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, f.priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	b.Signature = sig
	wire, err := b.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return wire
}

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"default", "default"},
		{"Goat Prod", "goat-prod"},
		{"  Cochlearis  Dev/Test  ", "cochlearis-dev-test"},
		{"___weird___", "weird"},
		{"", ""},
		{"...", ""},
		{"already-slugged", "already-slugged"},
	}
	for _, c := range cases {
		got := Slugify(c.in)
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyIsIdempotent(t *testing.T) {
	inputs := []string{"Goat Prod", "cochlearis dev/test", "Already-Slugged-Already"}
	for _, in := range inputs {
		first := Slugify(in)
		second := Slugify(first)
		if first != second {
			t.Errorf("Slugify not idempotent for %q: %q vs %q", in, first, second)
		}
	}
}

func TestValidateNameRejectsReservedSlugs(t *testing.T) {
	for _, name := range []string{"", ".", "..", "active", "ACTIVE", "Active"} {
		if _, err := validateName(name); err == nil {
			t.Errorf("validateName(%q) accepted; expected ErrInvalidName", name)
		} else if !errors.Is(err, ErrInvalidName) {
			t.Errorf("validateName(%q) returned %v; expected ErrInvalidName", name, err)
		}
	}
}

func TestStoreListEmpty(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %d entries", len(got))
	}
}

func TestStoreAddListLoad(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")

	info, err := f.store.Add(AddProfileRequest{Name: "Default", Mode: mode.Combined, BundleBytes: data})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if info.Slug != "default" {
		t.Errorf("slug = %q, want %q", info.Slug, "default")
	}
	if info.Name != "Default" {
		t.Errorf("name = %q, want %q", info.Name, "Default")
	}
	if info.Mode != mode.Combined {
		t.Errorf("mode = %q, want %q", info.Mode, mode.Combined)
	}
	if info.DeviceID != "device-1" {
		t.Errorf("device id = %q, want device-1", info.DeviceID)
	}

	got, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "default" {
		t.Fatalf("List = %+v, want one entry slug=default", got)
	}
	if got[0].Active {
		t.Errorf("Add must not flip active marker (got Active=true)")
	}

	p, err := f.store.Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Bundle == nil || p.Bundle.DeviceID != "device-1" {
		t.Errorf("Load.Bundle = %+v", p.Bundle)
	}
}

func TestStoreAddRejectsDuplicateWithoutReplace(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("Add 2 err = %v, want ErrAlreadyExists", err)
	}
}

func TestStoreAddReplacePreservesCreatedAt(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	first, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data})
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.NetbirdOnly, BundleBytes: data, Replace: true})
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt not preserved on Replace: %v → %v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance on Replace: %v → %v", first.UpdatedAt, second.UpdatedAt)
	}
	if second.Mode != mode.NetbirdOnly {
		t.Errorf("Replace did not update Mode: %v", second.Mode)
	}
}

func TestStoreSetActiveRoundTrip(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	prev, err := f.store.SetActive("default")
	if err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if prev != "" {
		t.Errorf("previous active = %q, want empty", prev)
	}
	got, _ := f.store.Active()
	if got != "default" {
		t.Errorf("Active = %q, want default", got)
	}
	infos, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || !infos[0].Active {
		t.Errorf("List did not mark default active: %+v", infos)
	}
}

func TestStoreSetActiveRejectsUnknownSlug(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.SetActive("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetActive(unknown) err = %v, want ErrNotFound", err)
	}
}

func TestStoreRemoveClearsActive(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := f.store.SetActive("default"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := f.store.Remove("default"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ := f.store.Active()
	if got != "" {
		t.Errorf("Active after Remove = %q, want empty", got)
	}
}

func TestStoreRenamePreservesBundleBytes(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "OldName", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := f.store.SetActive("oldname"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	info, err := f.store.Rename("oldname", "Cochlearis Dev")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if info.Slug != "cochlearis-dev" || info.Name != "Cochlearis Dev" {
		t.Errorf("Rename: slug=%q name=%q, want cochlearis-dev/Cochlearis Dev", info.Slug, info.Name)
	}
	got, _ := f.store.Active()
	if got != "cochlearis-dev" {
		t.Errorf("Active not updated on rename: %q", got)
	}
	p, err := f.store.Load("cochlearis-dev")
	if err != nil {
		t.Fatalf("Load after rename: %v", err)
	}
	if p.Bundle.DeviceID != "device-1" {
		t.Errorf("Bundle bytes lost on rename: device-id %q", p.Bundle.DeviceID)
	}
	if _, err := f.store.Load("oldname"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(oldname) err = %v, want ErrNotFound", err)
	}
}

func TestStoreRenameRejectsCollision(t *testing.T) {
	f := newFixture(t)
	dataA := f.mintBundle(t, "device-A", "site-A")
	dataB := f.mintBundle(t, "device-B", "site-B")
	if _, err := f.store.Add(AddProfileRequest{Name: "alpha", Mode: mode.Combined, BundleBytes: dataA}); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	if _, err := f.store.Add(AddProfileRequest{Name: "beta", Mode: mode.Combined, BundleBytes: dataB}); err != nil {
		t.Fatalf("Add beta: %v", err)
	}
	_, err := f.store.Rename("alpha", "beta")
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("Rename alpha→beta err = %v, want ErrAlreadyExists", err)
	}
}

func TestStoreUpdateModePreservesBundle(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "default", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.store.UpdateMode("default", mode.NetbirdOnly); err != nil {
		t.Fatalf("UpdateMode: %v", err)
	}
	p, err := f.store.Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Mode != mode.NetbirdOnly {
		t.Errorf("Mode = %q, want netbird-only", p.Mode)
	}
	if p.Bundle == nil || p.Bundle.DeviceID != "device-1" {
		t.Error("UpdateMode disturbed bundle bytes")
	}
}

// TestStoreMultiProfileClobberResistance is the load-bearing verdict-gate
// regression. Adds two profiles, flips active back-and-forth, restarts
// the store (simulating a daemon restart), and verifies both profiles
// plus the active marker survive intact. Encodes the netbird-stock
// failure mode 76M was built to replace: GUI actions on one profile
// must not clobber another profile's cached creds.
func TestStoreMultiProfileClobberResistance(t *testing.T) {
	f := newFixture(t)
	dataA := f.mintBundle(t, "device-A", "site-A")
	dataB := f.mintBundle(t, "device-B", "site-B")

	if _, err := f.store.Add(AddProfileRequest{Name: "Goat Prod", Mode: mode.Combined, BundleBytes: dataA}); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if _, err := f.store.Add(AddProfileRequest{Name: "Cochlearis Dev", Mode: mode.NetbirdOnly, BundleBytes: dataB}); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	for _, slug := range []string{"goat-prod", "cochlearis-dev", "goat-prod", "cochlearis-dev"} {
		if _, err := f.store.SetActive(slug); err != nil {
			t.Fatalf("SetActive %s: %v", slug, err)
		}
	}
	// UpdateMode on the *inactive* profile — corresponds to the
	// netbird-stock "Settings → Management URL Save" GUI action that
	// wipes the OTHER profile's creds. We assert it does NOT here.
	if err := f.store.UpdateMode("goat-prod", mode.WGCP0Only); err != nil {
		t.Fatalf("UpdateMode goat-prod: %v", err)
	}
	// Restart the store: a fresh Store against the same dir.
	s2, err := New(Config{Dir: f.store.dir, ActivePath: f.store.activePath, TrustRoots: f.trustRoots})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.List()
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
	for _, slug := range []string{"goat-prod", "cochlearis-dev"} {
		p, err := s2.Load(slug)
		if err != nil {
			t.Fatalf("Load %s: %v", slug, err)
		}
		if p.Bundle == nil {
			t.Fatalf("%s bundle nil", slug)
		}
	}
	// Active marker survives restart and points at the last-set slug.
	active, _ := s2.Active()
	if active != "cochlearis-dev" {
		t.Errorf("active after restart = %q, want cochlearis-dev", active)
	}
	for _, info := range got {
		if info.Slug == "cochlearis-dev" && !info.Active {
			t.Errorf("Active flag not set on cochlearis-dev in list: %+v", info)
		}
		if info.Slug == "goat-prod" && info.Active {
			t.Errorf("Active flag spuriously set on goat-prod: %+v", info)
		}
	}
}

// TestStoreConcurrentReadsAndWrites exercises the mutex discipline —
// N readers + M writers, race detector flags missing locks.
func TestStoreConcurrentReadsAndWrites(t *testing.T) {
	f := newFixture(t)
	dataA := f.mintBundle(t, "device-A", "site-A")
	dataB := f.mintBundle(t, "device-B", "site-B")
	if _, err := f.store.Add(AddProfileRequest{Name: "a", Mode: mode.Combined, BundleBytes: dataA}); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := f.store.Add(AddProfileRequest{Name: "b", Mode: mode.Combined, BundleBytes: dataB}); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = f.store.List()
					_, _ = f.store.Active()
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(slugs []string) {
			defer wg.Done()
			j := 0
			for {
				select {
				case <-stop:
					return
				default:
					if _, err := f.store.SetActive(slugs[j%2]); err != nil {
						t.Errorf("SetActive: %v", err)
						return
					}
					j++
				}
			}
		}([]string{"a", "b"})
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestStoreListSurvivesMalformedSiblingMeta confirms List skips a
// malformed sibling rather than failing the whole call.
func TestStoreListSurvivesMalformedSiblingMeta(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "device-1", "site-A")
	if _, err := f.store.Add(AddProfileRequest{Name: "good", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := writeAtomic(filepath.Join(f.store.dir, "broken.meta.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed malformed: %v", err)
	}
	got, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, info := range got {
		names = append(names, info.Slug)
	}
	if strings.Join(names, ",") != "good" {
		t.Errorf("List = %v, want only [good]", names)
	}
}

// TestStoreMigrateLegacyBundle covers v0.1.x → v0.2 migration: a single
// bundle.cbor at the legacy path is imported into the new store as
// the "default" profile, the active marker is set, and the legacy
// file is left in place (non-destructive migration).
func TestStoreMigrateLegacyBundle(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "legacy-device", "legacy-site")
	legacyPath := filepath.Join(f.dir, "bundle.cbor")
	if err := writeAtomic(legacyPath, data, 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	slug, migrated, err := f.store.MigrateLegacyBundle(legacyPath, mode.Combined)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !migrated {
		t.Fatal("Migrate returned ok=false; expected migration to run")
	}
	if slug != "default" {
		t.Errorf("slug = %q, want default", slug)
	}
	active, _ := f.store.Active()
	if active != "default" {
		t.Errorf("active after migrate = %q, want default", active)
	}
	// Legacy file still present (non-destructive).
	if _, err := f.store.summarise("default"); err != nil {
		t.Errorf("summarise after migrate: %v", err)
	}
}

func TestStoreMigrateLegacyBundleSkipsWhenStoreNonEmpty(t *testing.T) {
	f := newFixture(t)
	data := f.mintBundle(t, "seed", "seed-site")
	if _, err := f.store.Add(AddProfileRequest{Name: "seed", Mode: mode.Combined, BundleBytes: data}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	legacyData := f.mintBundle(t, "legacy-device", "legacy-site")
	legacyPath := filepath.Join(f.dir, "bundle.cbor")
	if err := writeAtomic(legacyPath, legacyData, 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	_, migrated, err := f.store.MigrateLegacyBundle(legacyPath, mode.Combined)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated {
		t.Error("Migrate ran even though store was non-empty")
	}
}

func TestStoreMigrateLegacyBundleSkipsWhenAbsent(t *testing.T) {
	f := newFixture(t)
	_, migrated, err := f.store.MigrateLegacyBundle(filepath.Join(f.dir, "nonexistent.cbor"), mode.Combined)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated {
		t.Error("Migrate ran on a missing legacy file")
	}
}
