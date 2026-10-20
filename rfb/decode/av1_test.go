package decode

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"testing"

	"github.com/si-mods/MyVNC/internal/mf"
	"github.com/si-mods/MyVNC/rfb/pixel"
)

func TestAV1Decode(t *testing.T) {
	if !AV1Available() {
		t.Skip("no AV1 Media Foundation decoder")
	}
	data, err := os.ReadFile("testdata/red.av1")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	binary.Write(&b, binary.BigEndian, uint32(av1ResetContext|av1ResetAllContexts))
	b.Write(data)
	fb := image.NewRGBA(image.Rect(0, 0, 64, 64))
	d := &AV1{Mode: mf.Software}
	defer d.Close()
	if err := d.Decode(bytes.NewReader(b.Bytes()), fb.Rect, &pixel.RGBA, fb); err != nil {
		t.Fatal(err)
	}
	o := fb.PixOffset(32, 32)
	if fb.Pix[o] < 200 || fb.Pix[o+1] > 30 || fb.Pix[o+2] > 30 {
		t.Errorf("center pixel %v, want red", fb.Pix[o:o+4])
	}
}
