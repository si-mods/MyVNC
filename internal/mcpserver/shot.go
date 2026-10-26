// SPDX-License-Identifier: GPL-2.0-or-later

package mcpserver

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
)

// Region is a box of framebuffer pixels.
type Region struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// cropRect is the part of a w×h framebuffer that reg names, all of it for
// nil; it is an error when nothing is left.
func cropRect(w, h int, reg *Region) (image.Rectangle, error) {
	full := image.Rect(0, 0, w, h)
	if reg == nil {
		return full, nil
	}
	if reg.W <= 0 || reg.H <= 0 {
		return image.Rectangle{}, fmt.Errorf("invalid_argument: region needs w and h above 0")
	}
	r := image.Rect(reg.X, reg.Y, reg.X+reg.W, reg.Y+reg.H).Intersect(full)
	if r.Empty() {
		return image.Rectangle{}, fmt.Errorf("invalid_argument: region is outside the %dx%d framebuffer", w, h)
	}
	return r, nil
}

// outSize is the size of a rw×rh region scaled by s.
func outSize(rw, rh int, s float64) (int, int) {
	return max(1, int(math.Round(float64(rw)*s))), max(1, int(math.Round(float64(rh)*s)))
}

// scaleRegion draws region r of src, scaled to ow×oh, averaging the source
// pixels each output pixel covers. The alpha byte of a framebuffer is not
// reliable, so the result is opaque.
func scaleRegion(src *image.RGBA, r image.Rectangle, ow, oh int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	rw, rh := r.Dx(), r.Dy()
	for oy := range oh {
		y0 := oy * rh / oh
		y1 := max((oy+1)*rh/oh, y0+1)
		for ox := range ow {
			x0 := ox * rw / ow
			x1 := max((ox+1)*rw/ow, x0+1)
			var sr, sg, sb, n uint32
			for y := y0; y < y1; y++ {
				o := src.PixOffset(r.Min.X+x0, r.Min.Y+y)
				for x := x0; x < x1; x++ {
					sr += uint32(src.Pix[o])
					sg += uint32(src.Pix[o+1])
					sb += uint32(src.Pix[o+2])
					o += 4
				}
				n += uint32(x1 - x0)
			}
			d := dst.PixOffset(ox, oy)
			dst.Pix[d], dst.Pix[d+1], dst.Pix[d+2], dst.Pix[d+3] = uint8(sr/n), uint8(sg/n), uint8(sb/n), 0xff
		}
	}
	return dst
}

var pngEnc = png.Encoder{CompressionLevel: png.BestSpeed}

// encodeImage returns img as JPEG (quality 70) or PNG, and its MIME type.
func encodeImage(img *image.RGBA, format string) ([]byte, string, error) {
	var b bytes.Buffer
	var err error
	mime := "image/jpeg"
	switch format {
	case "", "jpeg", "jpg":
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 70})
	case "png":
		mime, err = "image/png", pngEnc.Encode(&b, img)
	default:
		return nil, "", fmt.Errorf("invalid_argument: format must be jpeg or png")
	}
	return b.Bytes(), mime, err
}
