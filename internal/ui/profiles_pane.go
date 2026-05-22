package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// profilesPane is the v0.2 Block 76M multi-network management surface.
// Lists every stored profile with its Mode + site + expiry, lets the
// user pick which is active (the load-bearing switch action), rename,
// or remove. Adding a profile is the existing Bundle tab's job — that
// surface stays single-purpose so users still see "import a bundle"
// as the primary onboarding entry point.
//
// Critically, NONE of the actions on this pane wipe cached enrollment
// creds the way netbird-stock's Settings → Connection → Management URL
// edit does. Rename touches meta.json only; SetActive swaps the
// in-memory active pointer + writes active.json; Remove deletes the
// targeted profile's files but never another profile's. Bundle bytes
// (the cached creds) are only ever written through the Bundle pane's
// importBundle path.
type profilesPane struct {
	client ipc.Client
	win    fyne.Window

	list        *widget.List
	profiles    []ipc.ProfileInfo
	selectedIdx int

	switchBtn *widget.Button
	renameBtn *widget.Button
	removeBtn *widget.Button
	status    *widget.Label
	progress  *widget.ProgressBarInfinite

	onChanged func()

	// refreshDoneForTest mirrors the bundle/diagnostics/settings panes'
	// pattern: a worker goroutine signals here on completion so tests
	// can deterministically observe widget state. Nil in production.
	refreshDoneForTest chan<- struct{}
}

func newProfilesPane(client ipc.Client) *profilesPane {
	p := &profilesPane{
		client:      client,
		selectedIdx: -1,
		status:      widget.NewLabel(""),
		progress:    widget.NewProgressBarInfinite(),
	}
	p.progress.Stop()
	p.progress.Hide()

	p.list = widget.NewList(
		func() int {
			return len(p.profiles)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("placeholder")
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(p.profiles) {
				return
			}
			lbl := obj.(*widget.Label)
			prof := p.profiles[id]
			marker := "  "
			if prof.Active {
				marker = "✓ "
			}
			expires := ""
			if !prof.ExpiresAt.IsZero() {
				expires = "  (expires " + prof.ExpiresAt.Format("2006-01-02") + ")"
			}
			lbl.SetText(fmt.Sprintf("%s%s — %s · %s%s", marker, prof.Name, prof.Mode, prof.Site, expires))
		},
	)
	p.list.OnSelected = func(id widget.ListItemID) {
		p.selectedIdx = id
		p.updateButtonState()
	}

	p.switchBtn = widget.NewButton("Switch to selected", func() { p.switchToSelected() })
	p.renameBtn = widget.NewButton("Rename…", func() { p.renameSelected() })
	p.removeBtn = widget.NewButton("Remove…", func() { p.removeSelected() })
	p.updateButtonState()

	return p
}

func (p *profilesPane) SetWindow(w fyne.Window) { p.win = w }

// SetOnChanged registers a callback for any state-mutating action
// (switch, rename, remove). The main window uses this to refresh the
// status pane after a profile switch.
func (p *profilesPane) SetOnChanged(fn func()) { p.onChanged = fn }

func (p *profilesPane) Content() fyne.CanvasObject {
	help := widget.NewLabel(
		"Each profile is a verified bundle plus a per-profile mode. The active\n" +
			"profile drives the daemon; switching reuses cached creds (no setup-key\n" +
			"prompt). Add a profile by dropping a bundle on the Bundle tab.",
	)
	help.Wrapping = fyne.TextWrapWord

	buttons := container.NewHBox(p.switchBtn, p.renameBtn, p.removeBtn)
	footer := container.NewVBox(buttons, container.NewHBox(p.progress, p.status))
	return container.NewBorder(help, footer, nil, nil, p.list)
}

// Refresh polls the daemon for the profile list. Safe to call from
// any goroutine.
func (p *profilesPane) Refresh() {
	if p.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	profiles, err := p.client.ListProfiles(ctx)
	if err != nil {
		fyne.Do(func() {
			p.status.SetText("Failed to load profiles: " + err.Error())
		})
		return
	}
	fyne.Do(func() {
		p.profiles = profiles
		p.list.Refresh()
		p.updateButtonState()
	})
}

func (p *profilesPane) updateButtonState() {
	have := p.selectedIdx >= 0 && p.selectedIdx < len(p.profiles)
	if !have {
		p.switchBtn.Disable()
		p.renameBtn.Disable()
		p.removeBtn.Disable()
		return
	}
	prof := p.profiles[p.selectedIdx]
	if prof.Active {
		p.switchBtn.Disable()
	} else {
		p.switchBtn.Enable()
	}
	p.renameBtn.Enable()
	p.removeBtn.Enable()
}

func (p *profilesPane) selectedProfile() (ipc.ProfileInfo, bool) {
	if p.selectedIdx < 0 || p.selectedIdx >= len(p.profiles) {
		return ipc.ProfileInfo{}, false
	}
	return p.profiles[p.selectedIdx], true
}

func (p *profilesPane) switchToSelected() {
	prof, ok := p.selectedProfile()
	if !ok {
		return
	}
	p.switchBtn.Disable()
	p.renameBtn.Disable()
	p.removeBtn.Disable()
	p.progress.Show()
	p.progress.Start()
	p.status.SetText("Switching to " + prof.Name + "…")

	go func() {
		defer func() {
			if p.refreshDoneForTest != nil {
				p.refreshDoneForTest <- struct{}{}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		start := time.Now()
		prev, _, err := p.client.SetActiveProfile(ctx, prof.Slug)
		elapsed := time.Since(start)
		fyne.Do(func() {
			p.progress.Stop()
			p.progress.Hide()
			if err != nil {
				p.status.SetText("Failed: " + err.Error())
				if p.win != nil {
					dialog.ShowError(err, p.win)
				}
				p.updateButtonState()
				return
			}
			p.status.SetText(fmt.Sprintf("Switched %s → %s in %s", prev, prof.Slug, elapsed.Round(time.Millisecond)))
			if p.onChanged != nil {
				p.onChanged()
			}
		})
		p.Refresh()
	}()
}

func (p *profilesPane) renameSelected() {
	prof, ok := p.selectedProfile()
	if !ok {
		return
	}
	entry := widget.NewEntry()
	entry.SetText(prof.Name)
	form := dialog.NewForm(
		"Rename profile",
		"Rename",
		"Cancel",
		[]*widget.FormItem{{Text: "Name", Widget: entry}},
		func(submitted bool) {
			if !submitted {
				return
			}
			newName := entry.Text
			if newName == "" || newName == prof.Name {
				return
			}
			go func() {
				defer func() {
					if p.refreshDoneForTest != nil {
						p.refreshDoneForTest <- struct{}{}
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := p.client.RenameProfile(ctx, prof.Slug, newName); err != nil {
					fyne.Do(func() {
						if p.win != nil {
							dialog.ShowError(err, p.win)
						}
					})
					return
				}
				if p.onChanged != nil {
					fyne.Do(p.onChanged)
				}
				p.Refresh()
			}()
		},
		p.win,
	)
	form.Show()
}

func (p *profilesPane) removeSelected() {
	prof, ok := p.selectedProfile()
	if !ok {
		return
	}
	if p.win == nil {
		// Headless test path — just proceed.
		go p.doRemove(prof)
		return
	}
	msg := "Remove profile " + prof.Name + "?"
	if prof.Active {
		msg += "\n\nThis profile is currently active; its tunnels will be taken down."
	}
	dialog.NewConfirm("Remove profile", msg, func(confirm bool) {
		if !confirm {
			return
		}
		go p.doRemove(prof)
	}, p.win).Show()
}

func (p *profilesPane) doRemove(prof ipc.ProfileInfo) {
	defer func() {
		if p.refreshDoneForTest != nil {
			p.refreshDoneForTest <- struct{}{}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.client.RemoveProfile(ctx, prof.Slug); err != nil {
		fyne.Do(func() {
			if p.win != nil {
				dialog.ShowError(err, p.win)
			}
		})
		return
	}
	if p.onChanged != nil {
		fyne.Do(p.onChanged)
	}
	p.Refresh()
}
