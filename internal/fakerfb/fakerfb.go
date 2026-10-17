// Package fakerfb is an RFB 3.8 server without security that answers each
// FramebufferUpdateRequest with a full-screen update, as fast as the client
// asks: video at the client's pace, for measuring the viewer without a
// real server. The frames are encoded once, before serving, so that the
// server costs little more than writing them.
package fakerfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
)

// Encodings Frames knows.
const (
	Raw   = "raw"   // Raw (0), one rectangle
	Tight = "tight" // Tight JPEG (7), tiles of at most 2048×256
)

// frames is how many different frames are sent in turn.
const frames = 4

// Frames returns the FramebufferUpdate messages of a w×h screen showing
// moving bands, in encoding enc, for a client that set pixel.RGBA (R, G,
// B, X bytes), as MyVNC does.
func Frames(w, h int, enc string) ([][]byte, error) {
	if w <= 0 || h <= 0 || w > 0xffff || h > 0xffff {
		return nil, fmt.Errorf("fakerfb: bad size %dx%d", w, h)
	}
	var out [][]byte
	for f := range frames {
		img := picture(w, h, f)
		var b bytes.Buffer
		switch enc {
		case Raw:
			b.Write([]byte{0, 0, 0, 1})
			rect(&b, image.Rect(0, 0, w, h), 0)
			b.Write(img.Pix)
		case Tight:
			var tiles []image.Rectangle
			for y := 0; y < h; y += 256 {
				for x := 0; x < w; x += 2048 {
					tiles = append(tiles, image.Rect(x, y, min(x+2048, w), min(y+256, h)))
				}
			}
			b.Write([]byte{0, 0})
			binary.Write(&b, binary.BigEndian, uint16(len(tiles)))
			var j bytes.Buffer
			for _, t := range tiles {
				j.Reset()
				if err := jpeg.Encode(&j, img.SubImage(t), &jpeg.Options{Quality: 80}); err != nil {
					return nil, err
				}
				rect(&b, t, 7)
				b.WriteByte(0x90) // JPEG
				n := j.Len()
				switch {
				case n < 1<<7:
					b.WriteByte(byte(n))
				case n < 1<<14:
					b.Write([]byte{byte(n) | 0x80, byte(n >> 7)})
				default:
					b.Write([]byte{byte(n) | 0x80, byte(n>>7) | 0x80, byte(n >> 14)})
				}
				b.Write(j.Bytes())
			}
		default:
			return nil, fmt.Errorf("fakerfb: unknown encoding %q", enc)
		}
		out = append(out, b.Bytes())
	}
	return out, nil
}

func rect(b *bytes.Buffer, r image.Rectangle, enc int32) {
	binary.Write(b, binary.BigEndian, []uint16{uint16(r.Min.X), uint16(r.Min.Y), uint16(r.Dx()), uint16(r.Dy())})
	binary.Write(b, binary.BigEndian, enc)
}

// picture is frame f: diagonal bands with some noise, which JPEG
// compresses about as well as video.
func picture(w, h, f int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(f*2654435761 + 1)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			n := byte(seed >> 28)
			v := x + y + f*w/frames
			o := img.PixOffset(x, y)
			img.Pix[o] = byte(v) + n
			img.Pix[o+1] = byte(v>>1) + n
			img.Pix[o+2] = byte(x>>2) ^ byte(y>>2) + n
			img.Pix[o+3] = 0xff
		}
	}
	return img
}

// Serve accepts connections on l, one at a time, until l is closed, and
// streams msgs, the messages of Frames, to each.
func Serve(l net.Listener, w, h int, msgs [][]byte) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		ServeConn(conn, w, h, msgs)
	}
}

// ServeConn speaks to one client until it hangs up, and closes conn.
func ServeConn(conn net.Conn, w, h int, msgs [][]byte) error {
	defer conn.Close()
	r := bufio.NewReader(conn)
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	conn.Write([]byte("RFB 003.008\n"))
	if _, err := read(12); err != nil {
		return err
	}
	conn.Write([]byte{1, 1}) // None
	if _, err := read(1); err != nil {
		return err
	}
	conn.Write([]byte{0, 0, 0, 0})     // SecurityResult OK
	if _, err := read(1); err != nil { // ClientInit
		return err
	}
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, []uint16{uint16(w), uint16(h)})
	b.Write([]byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 0, 8, 16, 0, 0, 0})
	binary.Write(&b, binary.BigEndian, uint32(8))
	b.WriteString("fakerfb!")
	if _, err := conn.Write(b.Bytes()); err != nil {
		return err
	}

	// The client asks for an update as each one starts, so a request
	// is nearly always waiting: one token is enough.
	want := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			if _, ok := <-want; !ok {
				return
			}
			if _, err := conn.Write(msgs[i%len(msgs)]); err != nil {
				return
			}
		}
	}()
	defer func() { close(want); <-done }()
	skip := map[byte]int{0: 19, 4: 7, 5: 5}
	for {
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		switch t {
		case 2: // SetEncodings
			hdr, err := read(3)
			if err != nil {
				return err
			}
			_, err = read(4 * int(binary.BigEndian.Uint16(hdr[1:])))
			if err != nil {
				return err
			}
		case 3: // FramebufferUpdateRequest
			if _, err := read(9); err != nil {
				return err
			}
			select {
			case want <- struct{}{}:
			default:
			}
		case 6: // ClientCutText; negative lengths are extended clipboard
			hdr, err := read(7)
			if err != nil {
				return err
			}
			n := int32(binary.BigEndian.Uint32(hdr[3:]))
			if _, err := read(int(max(n, -n))); err != nil {
				return err
			}
		default:
			n, ok := skip[t]
			if !ok {
				return errors.New("fakerfb: unexpected client message " + fmt.Sprint(t))
			}
			if _, err := read(n); err != nil {
				return err
			}
		}
	}
}
