package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWinUITheme(t *testing.T) {
	dark := winUITheme(true, ui.Transparent, 1)
	if dark.Background != ui.Hex("#202020") || dark.Surface != ui.Hex("#2b2b2b") ||
		dark.Radius != 4 || dark.Spacing != 4 || dark.Font != "Segoe UI Variable Text" ||
		dark.FontSize != 14 {
		t.Errorf("dark theme %+v", dark)
	}
	if dark.Accent != ui.Hex("#4cc2ff") {
		t.Errorf("dark accent %v, want #4cc2ff", dark.Accent)
	}
	light := winUITheme(false, ui.Transparent, 1.25)
	if light.Background != ui.Hex("#f3f3f3") || light.Surface != ui.Hex("#ffffff") ||
		light.Radius != 4 || light.FontSize != 17.5 {
		t.Errorf("light theme %+v", light)
	}
	if light.Accent != ui.Hex("#0067c0") {
		t.Errorf("light accent %v, want #0067c0", light.Accent)
	}
	// The desktop accent wins, lightened in the dark.
	got := winUITheme(true, ui.Hex("#c00000"), 1)
	if got.Accent == ui.Hex("#c00000") {
		t.Error("dark accent was not lightened")
	}
	if got := winUITheme(false, ui.Hex("#c00000"), 1); got.Accent != ui.Hex("#c00000") {
		t.Errorf("light accent %v, want the desktop's", got.Accent)
	}
}
