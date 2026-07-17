package daemon

import (
	"context"
	"testing"

	"github.com/dlf-dds/goat-client/internal/bundle"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
	"github.com/dlf-dds/goat-client/internal/profile"
)

// innerMeshBundle returns a minimal bundle whose inner-mesh setup carries
// the given mint-time Rosenpass policy — enough for HasInnerMesh + FromBundle.
func innerMeshBundle(rpEnabled, rpPermissive bool) *bundle.EnrollmentBundle {
	return &bundle.EnrollmentBundle{
		Version:  2,
		DeviceID: "test-device",
		InnerMeshSetup: bundle.InnerMeshSetup{
			ManagementURL:       "https://mgmt.test.internal",
			SetupKey:            "setup-key-xyz",
			RosenpassEnabled:    rpEnabled,
			RosenpassPermissive: rpPermissive,
		},
	}
}

// TestMeshConfigOverlay: the daemon-held override wins over the bundle's
// mint-time policy; with no override the bundle default flows through.
func TestMeshConfigOverlay(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	b := innerMeshBundle(false, false) // bundle default: PQC off

	if cfg := d.meshConfig(b); cfg.RosenpassEnabled {
		t.Fatalf("no override: RosenpassEnabled=true, want bundle default false")
	}

	d.mu.Lock()
	d.rosenpassOverride = &rosenpassIntent{Enabled: true, Permissive: true}
	d.mu.Unlock()
	cfg := d.meshConfig(b)
	if !cfg.RosenpassEnabled || !cfg.RosenpassPermissive {
		t.Fatalf("override: got enabled=%t permissive=%t, want true/true", cfg.RosenpassEnabled, cfg.RosenpassPermissive)
	}
}

// TestSetRosenpassReconnectsWithOverride: a live toggle on an up mesh
// reconfigures it with the new intent (the down->up dance) and reports a
// clean diff against the previous effective (bundle) policy.
func TestSetRosenpassReconnectsWithOverride(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	b := innerMeshBundle(false, false) // bundle default: PQC off
	fake := innermesh.NewFake()

	d.mu.Lock()
	d.currentBundle = b
	d.currentMode = mode.NetbirdOnly
	d.mesh = fake
	d.mu.Unlock()
	if err := fake.Connect(context.Background()); err != nil {
		t.Fatalf("bring fake mesh up: %v", err)
	}

	reply, err := d.SetRosenpass(context.Background(), ipc.SetRosenpassRequest{Enabled: true, Permissive: true})
	if err != nil {
		t.Fatalf("SetRosenpass: %v", err)
	}
	if reply.PreviousEnabled || !reply.Enabled || !reply.Permissive {
		t.Errorf("reply diff: prevEnabled=%t enabled=%t permissive=%t, want false/true/true",
			reply.PreviousEnabled, reply.Enabled, reply.Permissive)
	}
	if lc := fake.LastConfig(); !lc.RosenpassEnabled || !lc.RosenpassPermissive {
		t.Errorf("mesh reconfigured with enabled=%t permissive=%t, want true/true", lc.RosenpassEnabled, lc.RosenpassPermissive)
	}
	if fake.State() != innermesh.StateUp {
		t.Errorf("mesh state=%v after toggle, want up", fake.State())
	}

	snap, err := d.GetInnerMeshStatus(context.Background())
	if err != nil {
		t.Fatalf("GetInnerMeshStatus: %v", err)
	}
	if !snap.RosenpassEnabled || !snap.RosenpassPermissive {
		t.Errorf("status intent enabled=%t permissive=%t, want true/true", snap.RosenpassEnabled, snap.RosenpassPermissive)
	}
}

// TestSetRosenpassNoReconnectWhenDown: toggling while the mesh is down
// stores the intent but does not force a bring-up.
func TestSetRosenpassNoReconnectWhenDown(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t, mode.NetbirdOnly)
	b := innerMeshBundle(true, true) // bundle default: PQC on
	fake := innermesh.NewFake()      // never Connect'd -> StateClosed

	d.mu.Lock()
	d.currentBundle = b
	d.currentMode = mode.NetbirdOnly
	d.mesh = fake
	d.mu.Unlock()

	reply, err := d.SetRosenpass(context.Background(), ipc.SetRosenpassRequest{Enabled: false, Permissive: false})
	if err != nil {
		t.Fatalf("SetRosenpass: %v", err)
	}
	if !reply.PreviousEnabled {
		t.Errorf("prevEnabled=%t, want true (bundle default)", reply.PreviousEnabled)
	}
	if reply.Enabled {
		t.Errorf("enabled=%t, want false", reply.Enabled)
	}
	if fake.State() == innermesh.StateUp {
		t.Errorf("mesh came up on a toggle-while-down; want it left down")
	}
	if cfg := d.meshConfig(b); cfg.RosenpassEnabled {
		t.Errorf("meshConfig still enabled after toggling off")
	}
}

// TestOverrideFromProfile maps a profile's persisted *bool override into
// the daemon intent, with nil meaning "follow the bundle."
func TestOverrideFromProfile(t *testing.T) {
	t.Parallel()
	if ov := overrideFromProfile(nil); ov != nil {
		t.Errorf("nil profile: got %+v, want nil", ov)
	}
	if ov := overrideFromProfile(&profile.Profile{}); ov != nil {
		t.Errorf("no override: got %+v, want nil", ov)
	}
	tr, fa := true, false
	ov := overrideFromProfile(&profile.Profile{RosenpassEnabled: &tr, RosenpassPermissive: &fa})
	if ov == nil || !ov.Enabled || ov.Permissive {
		t.Errorf("override on/strict: got %+v, want {Enabled:true Permissive:false}", ov)
	}
}
