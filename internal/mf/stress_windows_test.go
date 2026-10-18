package mf

import (
	"os"
	"runtime/debug"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// privateBytes is the process's commit charge, which a leaked texture,
// sample or buffer grows.
func privateBytes() uint64 {
	debug.FreeOSMemory()
	var c [10]uint64 // PROCESS_MEMORY_COUNTERS_EX on 64-bit: cb, then SIZE_Ts
	*(*uint32)(unsafe.Pointer(&c[0])) = uint32(unsafe.Sizeof(c))
	h, _ := syscall.GetCurrentProcess()
	syscall.NewLazyDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo").Call(uintptr(h), uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c))
	return c[9] // PrivateUsage
}

// TestDecodeStress decodes the Mac keyframe MYVNC_STRESS times (say 500)
// with each decoder, a new Decoder every 50 (MYVNC_STRESS_PER), and reports the time a
// picture takes and how the process's memory moved.
func TestDecodeStress(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("MYVNC_STRESS"))
	per := 50
	if p, _ := strconv.Atoi(os.Getenv("MYVNC_STRESS_PER")); p > 0 {
		per = p
	}
	if n <= 0 {
		t.Skip("MYVNC_STRESS is not set: the stress run is local only")
	}
	data, err := os.ReadFile("testdata/ard444.h265")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Mode{Software, Hardware} {
		var d *Decoder
		var spent time.Duration
		var before uint64
		for i := range n {
			if i%per == 0 {
				if d != nil {
					d.Close()
				}
				if d, err = NewDecoder(HEVC, m); err != nil {
					t.Fatal(err)
				}
			}
			if i == 50 {
				before = privateBytes() // after warm-up
			}
			start := time.Now()
			frames, err := d.Decode(data, int64(i)*166666)
			spent += time.Since(start)
			if err != nil || len(frames) != 1 {
				t.Fatalf("%v: decode %d: %d pictures, %v", m, i, len(frames), err)
			}
		}
		info := d.Info()
		d.Close()
		after := privateBytes()
		t.Logf("%v (%s, %s): %d pictures, %.1f ms each; private bytes %d MB after the first 50, %d MB at the end",
			m, info.Decoding(), info.Output, n, float64(spent.Microseconds())/1000/float64(n), before>>20, after>>20)
		if after > before+256<<20 {
			t.Errorf("%v: memory grew %d MB", m, (after-before)>>20)
		}
	}
}
