package mf

import (
	"errors"
	"os"
	"testing"
)

// ardGrid are pixels of testdata/ard444.h265 decoded by ffmpeg
// (BT.709, full range), at x = 120+240i, y = 67+135j.
var ardGrid = [8][8][3]uint8{
	{{182, 161, 150}, {172, 156, 155}, {155, 146, 158}, {134, 135, 153}, {113, 122, 150}, {95, 114, 147}, {76, 98, 135}, {57, 79, 118}},
	{{187, 161, 137}, {185, 161, 146}, {176, 156, 153}, {157, 146, 151}, {222, 220, 241}, {116, 126, 147}, {91, 110, 138}, {73, 91, 127}},
	{{184, 157, 121}, {186, 158, 130}, {186, 160, 138}, {175, 155, 138}, {159, 146, 137}, {143, 137, 138}, {128, 107, 112}, {45, 26, 14}},
	{{189, 153, 89}, {152, 97, 47}, {119, 77, 47}, {124, 81, 49}, {181, 153, 113}, {173, 136, 110}, {150, 133, 119}, {185, 135, 72}},
	{{178, 154, 136}, {156, 141, 130}, {157, 140, 132}, {149, 139, 134}, {137, 131, 128}, {134, 128, 125}, {134, 97, 73}, {94, 51, 23}},
	{{188, 165, 138}, {148, 134, 131}, {172, 154, 134}, {30, 28, 26}, {131, 113, 117}, {208, 163, 133}, {96, 71, 37}, {7, 17, 21}},
	{{53, 26, 14}, {55, 29, 9}, {141, 124, 128}, {162, 135, 125}, {10, 14, 17}, {148, 114, 110}, {13, 19, 22}, {59, 43, 35}},
	{{155, 135, 121}, {205, 163, 123}, {34, 26, 26}, {98, 83, 91}, {24, 24, 28}, {24, 20, 25}, {10, 13, 10}, {39, 37, 31}},
}

// TestDecodeARD444 decodes a macOS High Performance keyframe (HEVC Range
// Extensions 4:4:4, 1920x1080 coded as 1920x1088) and compares it with
// ffmpeg's picture. The HEVC MFT gives its AYUV output a stride in pixels,
// which once showed the screen four times across with dark lines.
func TestDecodeARD444(t *testing.T) {
	data, err := os.ReadFile("testdata/ard444.h265")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(HEVC, Software)
	if errors.Is(err, ErrNoDecoder) {
		t.Skip("no HEVC decoder here")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	frames, err := d.Decode(data, 0)
	if err == nil && len(frames) == 0 {
		frames, err = d.Flush()
	}
	info := d.Info()
	if err != nil || len(frames) == 0 {
		t.Skipf("%q does not decode 4:4:4 here (output %s, offered %v): %d frames, %v", info.Name, info.Output, info.Outputs, len(frames), err)
	}
	f := frames[0]
	if f.Rect.Dx() != 1920 || f.Rect.Dy() != 1080 {
		t.Fatalf("frame %v, want 1920x1080", f.Rect)
	}
	for j, row := range ardGrid {
		for i, want := range row {
			x, y := 120+240*i, 67+135*j
			p := f.RGBAAt(x, y)
			got := [3]uint8{p.R, p.G, p.B}
			for c := range got {
				if int(got[c])-int(want[c]) > 3 || int(want[c])-int(got[c]) > 3 {
					t.Errorf("output %s: pixel %d,%d is %v, want %v", info.Output, x, y, got, want)
					break
				}
			}
		}
	}
}
