package viewer

import (
	"context"
	"image"
	"os"
	"strconv"
	"strings"
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

// TestE2E connects to a real server, as MYVNC_E2E_ADDR=127.0.0.1:5955,
// and waits for a screen that is not black. MYVNC_E2E_USER and
// MYVNC_E2E_PASS give credentials, MYVNC_E2E_SECTYPES a comma-separated
// allow-list of security type numbers (default: None alone without a
// password, else MyVNC's default list).
func TestE2E(t *testing.T) {
	addr := os.Getenv("MYVNC_E2E_ADDR")
	if addr == "" {
		t.Skip("MYVNC_E2E_ADDR is not set")
	}
	user, pass := os.Getenv("MYVNC_E2E_USER"), os.Getenv("MYVNC_E2E_PASS")
	var types []uint32
	if pass == "" {
		types = []uint32{rfb.SecTypeNone}
	}
	if v := os.Getenv("MYVNC_E2E_SECTYPES"); v != "" {
		types = nil
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(f), 10, 32)
			if err != nil {
				t.Fatalf("MYVNC_E2E_SECTYPES: %v", err)
			}
			types = append(types, uint32(n))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := DefaultConfig()
	h := &e2eHandler{done: make(chan image.Point, 1)}
	c, err := rfb.Dial(ctx, addr, rfb.Options{
		Shared: true, Encodings: cfg.EncodingList(), Decoders: decode.New(),
		SecurityTypes: types,
		Username:      user,
		Password:      func() (string, string, error) { return user, pass, nil },
		VerifyHostKey: func([]byte, string) error { return nil }, // a test server
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	h.c = c
	defer c.Close()
	t.Logf("security type %s (%d), encrypted %v", SecTypeName(c.SecurityType()), c.SecurityType(), c.Encrypted())
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
