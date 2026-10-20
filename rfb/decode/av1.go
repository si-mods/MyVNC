// AV1 rectangle decoding for a future screen-content transport.
// SPDX-License-Identifier: GPL-2.0-or-later

package decode

import (
	"errors"
	"image"
	"io"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

// EncAV1 is a private encoding number for AV1 rectangles: u32 length,
// u32 flags and that many bytes of an AV1 sample. No stock RFB server
// sends it; it exists for a future native transport.
const EncAV1 = 51

const (
	av1ResetContext     = 1 << 0
	av1ResetAllContexts = 1 << 1
	maxAV1Contexts      = 16
)

// AV1Available reports whether Media Foundation has an AV1 decoder here.
var AV1Available = sync.OnceValue(func() bool {
	d, err := mf.NewDecoder(mf.AV1, mf.Software)
	if err != nil {
		return false
	}
	d.Close()
	return true
})

// AV1 decodes the same rectangle framing as H264, one decoder per
// rectangle, up to maxAV1Contexts.
type AV1 struct {
	Mode     mf.Mode
	contexts []*av1Context
	t        int64
	info     atomic.Pointer[mf.Info]
}

type av1Context struct {
	rect image.Rectangle
	dec  *mf.Decoder
}

func (a *AV1) Info() mf.Info {
	if i := a.info.Load(); i != nil {
		return *i
	}
	return mf.Info{}
}

func (a *AV1) Close() error {
	for _, c := range a.contexts {
		c.dec.Close()
	}
	a.contexts = nil
	return nil
}

func (a *AV1) Decode(r io.Reader, rect image.Rectangle, _ *pixel.Format, fb *image.RGBA) error {
	n, err := readU32(r)
	if err != nil {
		return err
	}
	flags, err := readU32(r)
	if err != nil {
		return err
	}
	if n > 64<<20 {
		return badFmt("AV1 rectangle too large")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := check(fb, rect); err != nil {
		return err
	}

	var ctx *av1Context
	if flags&av1ResetAllContexts != 0 {
		a.Close()
		if n == 0 {
			return nil
		}
	} else if i := slices.IndexFunc(a.contexts, func(c *av1Context) bool { return c.rect == rect }); i >= 0 {
		ctx = a.contexts[i]
		if flags&av1ResetContext != 0 {
			ctx.dec.Close()
			a.contexts = slices.Delete(a.contexts, i, i+1)
			ctx = nil
		}
	}
	if ctx == nil {
		if len(a.contexts) >= maxAV1Contexts {
			a.contexts[0].dec.Close()
			a.contexts = slices.Delete(a.contexts, 0, 1)
		}
		dec, err := mf.NewDecoder(mf.AV1, a.Mode)
		if err != nil {
			return errors.Join(errors.New("decode: failed to create AV1 context"), err)
		}
		ctx = &av1Context{rect, dec}
		a.contexts = append(a.contexts, ctx)
	}
	if n == 0 {
		return nil
	}
	a.t++
	frames, err := ctx.dec.Decode(data, a.t)
	info := ctx.dec.Info()
	a.info.Store(&info)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return nil
	}
	f := frames[len(frames)-1]
	if f.Rect.Dx() < rect.Dx() || f.Rect.Dy() < rect.Dy() {
		return badFmt("AV1 frame is smaller than its rectangle")
	}
	w := rect.Dx() * 4
	for y := range rect.Dy() {
		o := fb.PixOffset(rect.Min.X, rect.Min.Y+y)
		copy(fb.Pix[o:o+w], f.Pix[y*f.Stride:])
	}
	return nil
}
