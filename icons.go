// The app's icons: Lucide's, embedded as SVG files (ISC, see
// assets/icons/LICENSE).
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/icons/*.svg
var iconFiles embed.FS

var svgs = map[string]*ui.SVG{}

// icon returns the embedded icon name, as "settings" for settings.svg.
func icon(name string) *ui.SVG {
	if s, ok := svgs[name]; ok {
		return s
	}
	data, err := iconFiles.ReadFile("assets/icons/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	s := ui.MustParseSVG(data)
	svgs[name] = s
	return s
}
