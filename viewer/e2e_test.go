package viewer

import (
	"context"
	"image"
	"os"
	"testing"
	"time"

	"github.com/kyya/myvnc/rfb"
	"github.com/kyya/myvnc/rfb/decode"
)

// e2eHandler reports the first update that is not all black.
type e2eHandler struct {
	c    *rfb.Client
	done chan image.Point
}

func (h *e2eHandler) Resize(w, hh int)                {}
func (h *e2eHandler) Updated(image.Rectangle)         {}
func (h *e2eHandler) Cursor(*image.RGBA, image.Point) {}
func (h *e2eHandler) Bell()                           {}
func (h *e2eHandler) Clipboard(string)                {}
func (h *e2eHandler) Name(string)                     {}
func (h *e2eHandler) UpdateDone() {
	fb := h.c.Framebuffer()
	for i := 0; i < len(fb.Pix); i += 4 {
		if fb.Pix[i]|fb.Pix[i+1]|fb.Pix[i+2] != 0 {
			select {
			case h.done <- fb.Rect.Size():
			default:
			}
			return
		}
	}
}

// TestE2E connects to a real server, as MYVNC_E2E_ADDR=127.0.0.1:5955
// (no authentication), and waits for a screen that is not black.
func TestE2E(t *testing.T) {
	addr := os.Getenv("MYVNC_E2E_ADDR")
	if addr == "" {
		t.Skip("MYVNC_E2E_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := DefaultConfig()
	h := &e2eHandler{done: make(chan image.Point, 1)}
	c, err := rfb.Dial(ctx, addr, rfb.Options{
		Shared: true, Encodings: cfg.EncodingList(), Decoders: decode.New(),
		SecurityTypes: []uint32{rfb.SecTypeNone},
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	defer c.Close()
	errc := make(chan error, 1)
	go func() { errc <- c.Run() }()
	select {
	case size := <-h.done:
		t.Logf("non-black %v framebuffer", size)
	case err := <-errc:
		t.Fatalf("connection ended: %v", err)
	case <-ctx.Done():
		t.Fatal("no non-black update")
	}
}
