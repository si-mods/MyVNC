package rfb

import (
	"testing"

	"github.com/si-mods/MyVNC/rfb/ard/media"
)

func TestOfferFlags(t *testing.T) {
	for _, tc := range []struct {
		flags   uint32
		limit30 bool
		want    uint32
	}{
		{0, false, media.DefaultFlags},
		{0, true, media.FlagNoCursor},
		{0x0d, false, 0x0d},
		{0x0d, true, 0x0c},
		{media.FlagARD, true, media.FlagARD},
	} {
		if got := offerFlags(media.Params{Flags: tc.flags}, tc.limit30); got != tc.want {
			t.Errorf("flags %#x, limit %v: %#x, want %#x", tc.flags, tc.limit30, got, tc.want)
		}
	}
	c := &Client{hp: &hpState{}}
	c.LimitMediaFrameRate(true)
	if !c.hp.limit30.Load() {
		t.Error("LimitMediaFrameRate did not set")
	}
	(&Client{}).LimitMediaFrameRate(true) // no High Performance: nothing
}
