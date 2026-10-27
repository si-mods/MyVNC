// The Settings → Updates page, and the hooks the updater plugin gives it.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/locales"
)

// updateHooks is what the Updates page asks of the updater plugin; nil
// when this build has none, as in the tests and in --perftest runs.
type updateHooks struct {
	check   func()           // opens the update window and checks
	auto    func() bool      // whether checks run in the background
	setAuto func(on bool)    // turns them on or off
	last    func() time.Time // when the app last checked, zero for never
}

// updatesView is the Updates page: checking now, the automatic check, and
// when the app last checked. The page says why when the build cannot
// update itself.
func (a *app) updatesView(c *ui.Context, k *colors) {
	h := a.upds
	if h == nil {
		settingRow(c, k, locales.CheckForUpdates(), locales.UpdatesUnavailable(), func() {})
		return
	}
	settingRow(c, k, locales.CheckForUpdates(), locales.UpdateHint(), func() {
		if ui.Button(c, locales.CheckNow()).Clicked() {
			h.check()
		}
	})
	if h.auto != nil && h.setAuto != nil {
		on := h.auto()
		if settingSwitch(c, k, locales.AutoUpdate(), locales.AutoUpdateHint(), &on) {
			h.setAuto(on)
			a.svc.Invalidate() // the switch's own state is the plugin's
		}
	}
	if h.last != nil {
		settingRow(c, k, locales.LastCheckLabel(), "", func() {
			text := locales.NeverChecked()
			if when := h.last(); !when.IsZero() {
				text = locales.LastChecked(when.Local())
			}
			ui.Text(c, text).FontSize(12.5).TextColor(k.textMuted).SingleLine()
		})
	}
}
