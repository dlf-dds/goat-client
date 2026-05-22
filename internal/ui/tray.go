package ui

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/dlf-dds/goat-client/internal/ipc"
	"github.com/dlf-dds/goat-client/internal/mode"
)

// trayApp is the parent-process view: just a systray. The Fyne window
// runs as a separate child process spawned on demand. This mirrors the
// netbird upstream pattern and avoids the macOS main-thread conflict
// between fyne.io/systray's NSStatusItem and Fyne's NSApplication.
//
// v0.2: tray honors the current Mode. Single-tunnel modes show one icon
// + one Status line; combined mode shows a stacked icon + two Status
// lines (outer wg-cp0 + inner netbird) so operators can read both legs
// at a glance.
//
// v0.2 Block 76M: tray gains a Profiles submenu — one row per stored
// profile, checkmark on the active one. Clicking a row drives
// SetActiveProfile to switch. Replaces the netbird-stock-GUI profile
// picker that triggers SaaS Auth0 redirects + wipes cached creds.
type trayApp struct {
	addr   string
	client ipc.Client

	mu          sync.Mutex
	lastState   ipc.State
	lastInner   ipc.State
	lastMode    mode.Mode
	profileList []ipc.ProfileInfo
	activeSlug  string

	mMode       *systray.MenuItem
	mStatus     *systray.MenuItem
	mInner      *systray.MenuItem
	mProfiles   *systray.MenuItem // parent of the per-profile rows
	profileRows []*profileRow
	mConnect    *systray.MenuItem
	mDisconnect *systray.MenuItem
	mOpen       *systray.MenuItem
	mImport     *systray.MenuItem
	mQuit       *systray.MenuItem

	pollCancel context.CancelFunc
}

// profileRow is one submenu entry. The systray library doesn't let us
// remove items after AddSubMenuItem, so we pre-allocate a fixed pool
// + hide/show them as the profile list changes. Pool size is the
// max number of profiles the tray will surface; users with more
// should use the Settings → Profiles pane.
type profileRow struct {
	item *systray.MenuItem
	slug string
	done chan struct{} // signals the click-handler goroutine to exit
}

// trayProfileRowsCap caps the number of profile rows the tray
// pre-allocates. Bumping this is cheap (a few hidden menu items);
// the cap exists to keep the systray's accept-loop bounded.
const trayProfileRowsCap = 16

// RunTray runs the systray loop on the calling goroutine (which MUST be
// the main goroutine on macOS). Returns when the user picks Quit or the
// tray library exits.
func RunTray(addr string) error {
	client, err := ipc.NewClient(addr)
	if err != nil {
		return err
	}
	t := &trayApp{addr: addr, client: client, lastState: ipc.StateDisconnected}
	systray.Run(t.onReady, t.onExit)
	return nil
}

func (t *trayApp) onReady() {
	systray.SetIcon(iconForState(ipc.StateDisconnected))
	systray.SetTitle("")
	systray.SetTooltip("goat-client")

	t.mMode = systray.AddMenuItem("Mode: …", "Active v0.2 mode")
	t.mMode.Disable()
	t.mStatus = systray.AddMenuItem("wg-cp0: Disconnected", "Outer tunnel state")
	t.mStatus.Disable()
	t.mInner = systray.AddMenuItem("netbird: Disconnected", "Inner-mesh state")
	t.mInner.Disable()
	t.mInner.Hide()
	systray.AddSeparator()

	// Profiles submenu — populated by refreshProfiles() once we
	// poll. Hidden when the daemon has zero stored profiles (the
	// fresh-install case before the first bundle is imported).
	t.mProfiles = systray.AddMenuItem("Profiles", "Switch active network profile")
	t.mProfiles.Hide()
	t.profileRows = make([]*profileRow, trayProfileRowsCap)
	for i := range t.profileRows {
		item := t.mProfiles.AddSubMenuItem("", "")
		item.Hide()
		row := &profileRow{item: item, done: make(chan struct{})}
		t.profileRows[i] = row
		go t.watchProfileRow(row)
	}
	systray.AddSeparator()

	t.mConnect = systray.AddMenuItem("Connect", "Bring the active mode's tunnels up")
	t.mDisconnect = systray.AddMenuItem("Disconnect", "Bring the active mode's tunnels down")
	systray.AddSeparator()
	t.mOpen = systray.AddMenuItem("Open window...", "Open the goat-client window")
	t.mImport = systray.AddMenuItem("Import bundle...", "Open the bundle import dialog")
	systray.AddSeparator()
	t.mQuit = systray.AddMenuItem("Quit", "Quit goat-client")

	go t.handleClicks()
	t.startPolling()
}

// watchProfileRow runs one click-handler per pre-allocated profile
// submenu row. The row's slug is updated by refreshProfiles when the
// profile list changes; clicks read the current slug atomically off
// trayApp.mu so the switch always targets the row's currently-shown
// profile, not the one it was bound to at startup.
func (t *trayApp) watchProfileRow(row *profileRow) {
	for {
		select {
		case <-row.item.ClickedCh:
			t.mu.Lock()
			slug := row.slug
			t.mu.Unlock()
			if slug == "" {
				continue
			}
			t.switchProfile(slug)
		case <-row.done:
			return
		}
	}
}

func (t *trayApp) switchProfile(slug string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prev, _, err := t.client.SetActiveProfile(ctx, slug)
	if err != nil {
		log.Printf("tray: switch profile %q: %v", slug, err)
		return
	}
	if prev != slug {
		log.Printf("tray: switched profile %s → %s", prev, slug)
	}
	t.refresh(ctx)
}

func (t *trayApp) onExit() {
	t.stopPolling()
	for _, row := range t.profileRows {
		close(row.done)
	}
	if t.client != nil {
		_ = t.client.Close()
	}
}

func (t *trayApp) handleClicks() {
	for {
		select {
		case <-t.mConnect.ClickedCh:
			t.connect()
		case <-t.mDisconnect.ClickedCh:
			t.disconnect()
		case <-t.mOpen.ClickedCh:
			t.spawnWindow()
		case <-t.mImport.ClickedCh:
			// Same child binary; child opens to the Bundle tab.
			t.spawnWindow()
		case <-t.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (t *trayApp) connect() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.client.Connect(ctx); err != nil {
		log.Printf("tray: connect: %v", err)
	}
	t.refresh(ctx)
}

func (t *trayApp) disconnect() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.client.Disconnect(ctx); err != nil {
		log.Printf("tray: disconnect: %v", err)
	}
	t.refresh(ctx)
}

// spawnWindow execs a fresh copy of this binary with --window so the Fyne
// window runs in its own process. Stdout/stderr are inherited so log
// output is visible if the user launched goat-client from a terminal.
func (t *trayApp) spawnWindow() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("tray: locate executable: %v", err)
		return
	}
	cmd := exec.Command(exe, "--window", "--daemon-addr="+t.addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("tray: spawn window: %v", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func (t *trayApp) startPolling() {
	ctx, cancel := context.WithCancel(context.Background())
	t.pollCancel = cancel
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		t.refresh(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.refresh(ctx)
			}
		}
	}()
}

func (t *trayApp) stopPolling() {
	if t.pollCancel != nil {
		t.pollCancel()
		t.pollCancel = nil
	}
}

func (t *trayApp) refresh(ctx context.Context) {
	st, err := t.client.GetStatus(ctx)
	if err != nil {
		return
	}
	t.refreshProfiles(ctx)
	m, _ := mode.Parse(st.Mode)
	if !m.Valid() {
		m = mode.WGCP0Only
	}
	var innerState ipc.State
	if st.InnerMesh != nil {
		innerState = st.InnerMesh.State
	} else {
		innerState = ipc.StateDisconnected
	}
	t.mu.Lock()
	changed := st.State != t.lastState || innerState != t.lastInner || m != t.lastMode
	t.lastState = st.State
	t.lastInner = innerState
	t.lastMode = m
	t.mu.Unlock()

	if changed {
		systray.SetIcon(iconForMode(m, st.State, innerState))
	}
	if t.mMode != nil {
		t.mMode.SetTitle("Mode: " + m.Display())
	}
	// Single-tunnel modes hide the leg that isn't running; combined mode
	// shows both.
	if t.mStatus != nil {
		if m.IncludesWGCP0() {
			t.mStatus.SetTitle("wg-cp0: " + stateLabel(st.State))
			t.mStatus.Show()
		} else {
			t.mStatus.Hide()
		}
	}
	if t.mInner != nil {
		if m.IncludesNetbird() {
			t.mInner.SetTitle("netbird: " + stateLabel(innerState))
			t.mInner.Show()
		} else {
			t.mInner.Hide()
		}
	}
	if t.mConnect != nil && t.mDisconnect != nil {
		// "Connected" in combined mode means both legs are up. Treat
		// either leg connecting as "in flight" so we don't show Connect.
		anyUp := false
		anyInFlight := false
		if m.IncludesWGCP0() {
			switch st.State {
			case ipc.StateConnected:
				anyUp = true
			case ipc.StateConnecting:
				anyInFlight = true
			}
		}
		if m.IncludesNetbird() {
			switch innerState {
			case ipc.StateConnected:
				anyUp = true
			case ipc.StateConnecting:
				anyInFlight = true
			}
		}
		if anyUp || anyInFlight {
			t.mConnect.Disable()
			t.mDisconnect.Enable()
		} else {
			t.mConnect.Enable()
			t.mDisconnect.Disable()
		}
	}
}

// refreshProfiles polls the daemon for the current profile list and
// rewrites the tray submenu rows in place. Hides the Profiles parent
// when the store is empty (a fresh install with no bundle imported
// yet). Visible-row count is capped at trayProfileRowsCap; extra
// profiles are accessible via the Settings → Profiles pane only.
func (t *trayApp) refreshProfiles(ctx context.Context) {
	profiles, err := t.client.ListProfiles(ctx)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(profiles) == 0 {
		t.mProfiles.Hide()
		t.profileList = nil
		t.activeSlug = ""
		for _, row := range t.profileRows {
			row.slug = ""
			row.item.Hide()
		}
		return
	}
	t.mProfiles.Show()
	t.profileList = profiles
	t.activeSlug = ""
	for i, row := range t.profileRows {
		if i < len(profiles) {
			p := profiles[i]
			label := p.Name
			if p.Active {
				label = "✓ " + p.Name
				t.activeSlug = p.Slug
			}
			row.item.SetTitle(label)
			row.item.SetTooltip(fmt.Sprintf("Switch to %s (mode: %s)", p.Name, p.Mode))
			row.slug = p.Slug
			row.item.Show()
			// The active row is the no-op click target; disable it so
			// the user gets visual feedback that there's nothing to
			// click. (systray re-enables on next refresh if active
			// changes.)
			if p.Active {
				row.item.Disable()
			} else {
				row.item.Enable()
			}
		} else {
			row.slug = ""
			row.item.Hide()
		}
	}
}
