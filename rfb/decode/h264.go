// Derived from TigerVNC common/rfb/H264Decoder.cxx (GPL-2.0-or-later).
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

// EncH264 is TigerVNC's H.264 encoding.
const EncH264 = 50

const (
	h264ResetContext     = 1 << 0
	h264ResetAllContexts = 1 << 1
	maxH264Contexts      = 64
)

// H264Available reports whether Media Foundation decodes H.264 here
// (Windows only); New lists H264 only then.
var H264Available = sync.OnceValue(func() bool {
	d, err := mf.NewDecoder(mf.H264, mf.Software)
	if err != nil {
		return false
	}
	d.Close()
	return true
})

// H264 is the H.264 encoding (50): each rectangle is a u32 length, u32
// flags and that many bytes of Annex B stream. Every distinct rectangle
// is a stream of its own, with its own decoder, up to 64; flag 1 resets
// the rectangle's stream, flag 2 all of them. Close releases them.
type H264 struct {
	Mode     mf.Mode        // where to decode
	contexts []*h264Context // oldest first
	t        int64
	info     atomic.Pointer[mf.Info]
}

// Info describes the decoder of the last rectangle decoded; zero before.
// Any goroutine.
func (h *H264) Info() mf.Info {
	if i := h.info.Load(); i != nil {
		return *i
	}
	return mf.Info{}
}

type h264Context struct {
	rect image.Rectangle
	dec  *mf.Decoder
}

func (h *H264) Close() error {
	for _, c := range h.contexts {
		c.dec.Close()
	}
	h.contexts = nil
	return nil
}

func (h *H264) Decode(r io.Reader, rect image.Rectangle, _ *pixel.Format, fb *image.RGBA) error {
	n, err := readU32(r)
	if err != nil {
		return err
	}
	flags, err := readU32(r)
	if err != nil {
		return err
	}
	if n > 64<<20 {
		return badFmt("H.264 rectangle too large")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := check(fb, rect); err != nil {
		return err
	}

	var ctx *h264Context
	i := -1
	if flags&h264ResetAllContexts != 0 {
		h.Close()
		if n == 0 {
			return nil
		}
	} else if i = slices.IndexFunc(h.contexts, func(c *h264Context) bool { return c.rect == rect }); i >= 0 {
		ctx = h.contexts[i]
		if flags&h264ResetContext != 0 {
			ctx.dec.Close()
			h.contexts = slices.Delete(h.contexts, i, i+1)
			ctx = nil
		}
	}
	if ctx == nil {
		if len(h.contexts) >= maxH264Contexts {
			h.contexts[0].dec.Close()
			h.contexts = slices.Delete(h.contexts, 0, 1)
		}
		dec, err := mf.NewDecoder(mf.H264, h.Mode)
		if err != nil {
			return errors.Join(errors.New("decode: failed to create H.264 context"), err)
		}
		ctx = &h264Context{rect, dec}
		h.contexts = append(h.contexts, ctx)
	}
	if n == 0 {
		return nil
	}

	h.t++
	frames, err := ctx.dec.Decode(data, h.t)
	info := ctx.dec.Info()
	h.info.Store(&info)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return nil // parameter sets only, or the picture is still held
	}
	f := frames[len(frames)-1]
	if f.Rect.Dx() < rect.Dx() || f.Rect.Dy() < rect.Dy() {
		return badFmt("H.264 frame is smaller than its rectangle")
	}
	w := rect.Dx() * 4
	for y := range rect.Dy() {
		o := fb.PixOffset(rect.Min.X, rect.Min.Y+y)
		copy(fb.Pix[o:o+w], f.Pix[y*f.Stride:])
	}
	return nil
}
