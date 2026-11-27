package timecode

import (
	"testing"

	"archivecheck/internal/timebase"
)

// FuzzParse checks that arbitrary text is either accepted as a consistent
// value or rejected; it never panics and never returns a value without an
// error or an error without an offset.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"00:00:00:00", "00:10:00;00", "23:59:59:23", "01:02:03:04",
		"-00:00:01:00", "00:61:00:00", "00:00:60:00", "00:00:00:25", "99:59:59:00",
	} {
		f.Add(seed)
	}
	rates := []timebase.Rate{timebase.FPS24, timebase.FPS25, timebase.FPS2997, timebase.FPS50}
	f.Fuzz(func(t *testing.T, text string) {
		for _, rate := range rates {
			v, err := Parse(text, rate)
			if err != nil {
				if fe := asFault(err); fe == nil {
					t.Fatalf("%q: error without an offset: %v", text, err)
				}
				continue
			}
			if v.Frames < 0 || v.Nanos < 0 || v.Nanos > timebase.MaxNanos {
				t.Fatalf("%q: accepted out-of-range value %+v", text, v)
			}
			want, err := rate.TimecodeToNanos(v.Frames)
			if err != nil || want != v.Nanos {
				t.Fatalf("%q: inconsistent nanoseconds %d vs %d", text, v.Nanos, want)
			}
		}
	})
}
