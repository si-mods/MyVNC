// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/si-mods/MyVNC/internal/win32"
	"github.com/si-mods/MyVNC/locales"
	"github.com/si-mods/MyVNC/viewer"
)

// The choices of the Edit dialog of a Remote Desktop computer.
const (
	resFollow = "follow" // the size of the window
	resCustom = "custom" // the width and height typed in
)

// rdpSizes are the sizes the dialog offers, besides Follow window and Custom.
var rdpSizes = [][2]int{{1280, 720}, {1366, 768}, {1600, 900}, {1920, 1080}, {2560, 1440}, {3840, 2160}}

// rdpScales are the desktop scales it offers, in percent; 0 follows this PC.
var rdpScales = []int{0, 100, 125, 150, 175, 200, 250, 300}

// rdpResolutions are the values of the resolution select.
var rdpResolutions = func() []string {
	v := []string{resFollow}
	for _, s := range rdpSizes {
		v = append(v, sizeText(s[0], s[1]))
	}
	return append(v, resCustom)
}()

func sizeText(w, h int) string { return fmt.Sprintf("%d×%d", w, h) }

// errBadSize is what a size outside 200-8192, or of an odd width, gets.
var errBadSize = errors.New("bad size")

// rdpForm is the Edit dialog's state: the resolution choice (resFollow,
// resCustom or a size of rdpSizes), the typed width and height, the scale.
type rdpForm struct {
	res  string
	w, h string
	zoom int
	// local is the "scale the picture on this PC" switch.
	local bool
}

// newRDPForm shows what is saved: width, height and scale of a computer.
func newRDPForm(w, h, scale int, local bool) rdpForm {
	f := rdpForm{res: resFollow, zoom: scale, local: local}
	if w > 0 && h > 0 {
		f.res, f.w, f.h = resCustom, strconv.Itoa(w), strconv.Itoa(h)
		if s := sizeText(w, h); slices.Contains(rdpResolutions, s) {
			f.res = s
		}
	}
	return f
}

// parse turns the form into what Computer keeps; a width and height of 0
// follow the window.
func (f rdpForm) parse() (w, h, scale int, err error) {
	scale = f.zoom
	switch f.res {
	case resFollow:
		return 0, 0, scale, nil
	case resCustom:
		w, err = strconv.Atoi(strings.TrimSpace(f.w))
		if err == nil {
			h, err = strconv.Atoi(strings.TrimSpace(f.h))
		}
	default:
		_, err = fmt.Sscanf(f.res, "%d×%d", &w, &h)
	}
	if err != nil || w < 200 || w > 8192 || h < 200 || h > 8192 || w%2 != 0 {
		return 0, 0, 0, errBadSize
	}
	return w, h, scale, nil
}

// localDesktop is the remote desktop to ask for when this PC scales the
// picture by scale percent: the window's pixels divided by the scale, the
// width even, both within 200-8192.
func localDesktop(w, h, scale int32) (int32, int32) {
	if scale < 100 {
		scale = 100
	}
	w, h = w*100/scale, h*100/scale
	w &^= 1
	return min(max(w, 200), 8192), min(max(h, 200), 8192)
}

// editRDP opens the Edit dialog on server.
func (a *app) editRDP(server string) {
	c := a.cfg.Computers[server]
	a.editFor, a.editForm, a.editErr, a.editOpen = server, newRDPForm(c.Width, c.Height, c.Scale, c.LocalScale), "", true
	a.editHasPass = false
	if t, err := viewer.ParseURL(server); err == nil {
		cr, ok := a.loadCredential(t.Addr())
		a.editHasPass = ok && cr.Pass != ""
	}
}

// clearEditPassword deletes the stored password of the computer being
// edited; its account name stays.
func (a *app) clearEditPassword() {
	if t, err := viewer.ParseURL(a.editFor); err == nil {
		a.forgetCredential(t.Addr())
	}
	a.editHasPass, a.credRows = false, nil
}

// editView is the Edit dialog of a Remote Desktop computer: the size of its
// desktop and its scale, which apply at the next connection.
func (a *app) editView(c *ui.Context, k *colors) {
	title := a.editFor
	if n := a.cfg.Computers[a.editFor].Name; n != "" {
		title = n
	}
	dialog(c, k, &a.editOpen, title, 420, func() {
		ui.Text(c, title).FontSize(18).FontWeight(700).TextColor(k.text)
		ui.Field(c, locales.Resolution(), func() {
			selectValue(c, &a.editForm.res, rdpResolutions, func(v string) string {
				switch v {
				case resFollow:
					return locales.FollowWindow()
				case resCustom:
					return locales.CustomSize()
				}
				return v
			})
		})
		if a.editForm.res == resCustom {
			ui.Row(c).Gap(8).Children(func() {
				ui.Field(c, locales.SizeWidth(), func() { ui.TextInput(c, &a.editForm.w) })
				ui.Field(c, locales.SizeHeight(), func() { ui.TextInput(c, &a.editForm.h) })
			})
		}
		ui.Field(c, locales.DesktopScale(), func() {
			selectValue(c, &a.editForm.zoom, rdpScales, func(v int) string {
				if v == 0 {
					return locales.FollowThisPC()
				}
				return fmt.Sprintf("%d%%", v)
			})
		})
		settingSwitch(c, k, locales.LocalScale(), locales.LocalScaleHint(), &a.editForm.local)
		if a.editHasPass && ui.Button(c, locales.ClearSavedPassword()).Clicked() {
			a.clearEditPassword()
		}
		if a.editErr != "" {
			ui.Text(c, a.editErr).FontSize(12.5).TextColor(k.danger).Wrap()
		}
		ui.Text(c, locales.AppliesNextConnection()).FontSize(12).TextColor(k.textFaint).Wrap()
		ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
			if ui.Button(c, locales.Cancel()).Clicked() {
				a.editOpen = false
			}
			if ui.PrimaryButton(c, locales.Save()).Clicked() {
				a.saveEdit()
			}
		})
	})
}

// saveEdit keeps the dialog's choices for the computer, or shows why not.
func (a *app) saveEdit() {
	w, h, scale, err := a.editForm.parse()
	if err != nil {
		a.editErr = locales.BadSize()
		return
	}
	c := a.cfg.Computers[a.editFor]
	c.Width, c.Height, c.Scale, c.LocalScale = w, h, scale, a.editForm.local
	a.cfg.Computers[a.editFor] = c
	a.saveConfig()
	a.editOpen = false
}

// localScale is the scale percent this PC applies to the picture of t: the
// computer's own, or the DPI of the window.
func (t *tab) localScale(r *win32.RDP) int32 {
	if t.rdpScale > 0 {
		return t.rdpScale
	}
	return r.DPIPercent()
}
