package viewer

import (
	"image"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/ard/media"
)

// bandComposer decodes a compound picture's bands and stacks them into
// one RGBA. The Mac's multi-band stream (tilesPerFrame > 1) carries each
// horizontal band as its own HEVC bitstream on its own SSRC; media's
// compound Unit hands them over in SSRC order, top band first. Each band
// keeps a decoder of its own, as the bands' references are independent.
type bandComposer struct {
	codec media.Codec
	mode  mf.Mode
	bands []videoDecoder
}

// setCodec opens or reopens the band decoders when the picture's codec
// changed. n is the band count from the unit's BandSizes.
func (c *bandComposer) setCodec(codec media.Codec, n int, m mf.Mode) error {
	if c.codec == codec && len(c.bands) == n && c.mode == m {
		return nil
	}
	c.close()
	for range n {
		d, err := newVideoDecoder(codec, m)
		if err != nil {
			c.close()
			return err
		}
		c.bands = append(c.bands, d)
	}
	c.codec, c.mode = codec, m
	return nil
}

// close frees the band decoders.
func (c *bandComposer) close() {
	for _, d := range c.bands {
		if d != nil {
			d.Close()
		}
	}
	c.bands = nil
	c.codec = 0
}

// bandNALs splits the unit's NALs into one bitstream per band, from the
// BandSizes the compound recorded.
func bandNALs(u *media.Unit) [][][]byte {
	if len(u.BandSizes) == 0 {
		return nil
	}
	at, out := 0, make([][][]byte, 0, len(u.BandSizes))
	for _, n := range u.BandSizes {
		if at+n > len(u.BandNALs) {
			return nil
		}
		out = append(out, u.BandNALs[at:at+n])
		at += n
	}
	return out
}

// decode decodes each band and stacks the last pictures top to bottom. A
// band that decodes to nothing (still waiting for its IDR) makes the
// whole picture nil: showing half a desktop is worse than waiting. The
// returned info is the first band's.
func (c *bandComposer) decode(u *media.Unit, t int64) (*image.RGBA, mf.Info, error) {
	parts := bandNALs(u)
	if parts == nil {
		return nil, mf.Info{}, errNotCompound
	}
	if err := c.setCodec(u.Codec, len(parts), c.mode); err != nil {
		return nil, mf.Info{}, err
	}
	var (
		stack  *image.RGBA
		width  int
		height int
		info   mf.Info
		frames = make([]mf.Frame, len(parts))
	)
	for i, nals := range parts {
		fs, err := c.bands[i].Decode(annexB(nals), t)
		if err != nil {
			return nil, mf.Info{}, err
		}
		if len(fs) == 0 {
			return nil, mf.Info{}, nil // the band's IDR has not come yet
		}
		frames[i] = fs[len(fs)-1]
		if i == 0 {
			info = c.bands[0].Info()
		}
		w, h := frames[i].RGBA.Rect.Dx(), frames[i].RGBA.Rect.Dy()
		if w > width {
			width = w
		}
		height += h
	}
	if height == 0 {
		return nil, mf.Info{}, nil
	}
	stack = image.NewRGBA(image.Rect(0, 0, width, height))
	y := 0
	for _, f := range frames {
		for row := 0; row < f.Rect.Dy(); row++ {
			src := f.Pix[row*f.Stride : row*f.Stride+f.Rect.Dx()*4]
			copy(stack.Pix[(y+row)*stack.Stride:], src)
		}
		y += f.Rect.Dy()
	}
	return stack, info, nil
}

// annexB is NAL units with 00 00 00 01 start codes, as Unit.AnnexB does
// for the whole picture.
func annexB(nals [][]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(append(b, 0, 0, 0, 1), n...)
	}
	return b
}

var errNotCompound = errNotCompoundImpl{}

type errNotCompoundImpl struct{}

func (errNotCompoundImpl) Error() string { return "not a compound picture" }
