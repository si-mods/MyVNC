package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAppTheme(t *testing.T) {
	dark := themeFor(true, 1)
	if !dark.Dark || dark.Background != darkColors.surface || dark.Text != darkColors.text ||
		dark.Accent != ui.Hex("#6cb2ff") || dark.Radius != 9 || dark.Spacing != 4 ||
		dark.Font != "Segoe UI Variable Text" || dark.FontSize != 14 {
		t.Errorf("dark theme %+v", dark)
	}
	light := themeFor(false, 1.25)
	if light.Dark || light.Background != lightColors.surface || light.Text != lightColors.text ||
		light.Accent != ui.Hex("#2e6fd0") || light.FontSize != 17.5 {
		t.Errorf("light theme %+v", light)
	}
	if light.Danger != lightColors.danger || light.Warning != lightColors.warn || light.Success != lightColors.ok {
		t.Errorf("light levels %v %v %v", light.Danger, light.Warning, light.Success)
	}
	if themeFor(false, 0).FontSize != 14 {
		t.Error("no text scale is not 1")
	}
}
