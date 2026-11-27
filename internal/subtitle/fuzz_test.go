package subtitle

import (
	"testing"

	"archivecheck/internal/timebase"
)

// FuzzParse checks that arbitrary subtitle bytes never panic and that every
// recovered cue and problem carries a plausible offset inside the input.
func FuzzParse(f *testing.F) {
	f.Add([]byte(""), true)
	f.Add([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nhi\r\n"), true)
	f.Add([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n"), false)
	f.Add([]byte("WEBVTT\n\nid\n00:00:01.000 --> 00:00:0X.000 line:0\nhi\n"), false)
	f.Fuzz(func(t *testing.T, data []byte, srt bool) {
		name := "fuzz.vtt"
		if srt {
			name = "fuzz.srt"
		}
		tr := Parse(name, data)
		for _, c := range tr.Cues {
			if c.Offset < 0 || c.Offset > int64(len(data)) {
				t.Fatalf("cue offset %d outside %d bytes", c.Offset, len(data))
			}
			if c.Start < 0 || c.End < c.Start || c.End > timebase.MaxNanos {
				t.Fatalf("cue time out of range: %+v", c)
			}
		}
		for _, p := range tr.Problems {
			if p.Offset < 0 || p.Offset > int64(len(data)) {
				t.Fatalf("problem offset %d outside %d bytes", p.Offset, len(data))
			}
		}
	})
}
