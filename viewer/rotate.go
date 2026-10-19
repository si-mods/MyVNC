// Local picture rotation, so a viewer can show a landscape desktop on a
// portrait display (PiKVM's Orientation).
// SPDX-License-Identifier: GPL-2.0-or-later

package viewer

import "image"

// rotateImage copies src into dst rotated clockwise by deg (0, 90, 180
// or 270), allocating dst when its size does not match. It returns dst.
func rotateImage(dst, src *image.RGBA, deg int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := sw, sh
	if deg%180 != 0 {
		dw, dh = sh, sw
	}
	if dst == nil || dst.Rect.Dx() != dw || dst.Rect.Dy() != dh {
		dst = image.NewRGBA(image.Rect(0, 0, dw, dh))
	}
	if deg == 0 {
		copy(dst.Pix, src.Pix)
		return dst
	}
	sp, dp := src.Pix, dst.Pix
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
			}
			si := src.PixOffset(x, y)
			di := dst.PixOffset(dx, dy)
			copy(dp[di:di+4], sp[si:si+4])
		}
	}
	return dst
}

// unrotatePoint maps a point of the rotated picture (dw×dh) back to the
// source picture that rotateImage turned clockwise by deg.
func unrotatePoint(x, y, dw, dh, deg int) (int, int) {
	switch deg {
	case 90:
		// Display is dh×dw; dst(x,y) = src(y, dw-1-x).
		return y, dw - 1 - x
	case 180:
		return dw - 1 - x, dh - 1 - y
	case 270:
		// Display is dh×dw; dst(x,y) = src(dh-1-y, x).
		return dh - 1 - y, x
	}
	return x, y
}
