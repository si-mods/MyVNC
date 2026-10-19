// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import (
	"image"
	"image/color"
	"testing"
)

func TestRotateImage(t *testing.T) {
	// A 3×2 source: each pixel knows its own coordinates.
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), A: 0xff})
		}
	}
	for _, deg := range []int{0, 90, 180, 270} {
		dst := rotateImage(nil, src, deg)
		// Walk every source pixel to the place the rotation puts it.
		for y := 0; y < 2; y++ {
			for x := 0; x < 3; x++ {
				var dx, dy int
				switch deg {
				case 90:
					dx, dy = 1-y, x
				case 180:
					dx, dy = 2-x, 1-y
				case 270:
					dx, dy = y, 2-x
				default:
					dx, dy = x, y
				}
				got := dst.RGBAAt(dx, dy)
				if got.R != uint8(x) || got.G != uint8(y) {
					t.Errorf("%d°: source (%d,%d) at (%d,%d) = %v", deg, x, y, dx, dy, got)
				}
			}
		}
		dw, dh := dst.Rect.Dx(), dst.Rect.Dy()
		if deg%180 != 0 && (dw != 2 || dh != 3) || deg%180 == 0 && (dw != 3 || dh != 2) {
			t.Errorf("%d°: size %dx%d", deg, dw, dh)
		}
	}
}

func TestUnrotatePoint(t *testing.T) {
	const sw, sh = 3, 2
	for _, deg := range []int{0, 90, 180, 270} {
		dw, dh := sw, sh
		if deg%180 != 0 {
			dw, dh = sh, sw
		}
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				var dx, dy int
				switch deg {
				case 90:
					dx, dy = sh-1-y, x
				case 180:
					dx, dy = sw-1-x, sh-1-y
				case 270:
					dx, dy = y, sw-1-x
				default:
					dx, dy = x, y
				}
				gx, gy := unrotatePoint(dx, dy, dw, dh, deg)
				if gx != x || gy != y {
					t.Errorf("%d°: source (%d,%d) -> display (%d,%d) -> (%d,%d)", deg, x, y, dx, dy, gx, gy)
				}
			}
		}
	}
}
