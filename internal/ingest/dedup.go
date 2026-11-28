package ingest

// DeduplicateTracks merges tracks that describe the same logical media across
// containers (the project sample carries one logical set described once in
// MP4 and once in MXF). Containers are processed in order; the first container
// carrying a given content signature wins, so the MP4 description anchors the
// timeline while the MXF parse still validates the same metadata independently.
func DeduplicateTracks(containers ...*Container) []*Track {
	seen := map[string]int{}
	var out []*Track
	for _, c := range containers {
		if c == nil {
			continue
		}
		for _, t := range c.Tracks {
			sig := t.contentSignature()
			if _, dup := seen[sig]; dup {
				continue
			}
			seen[sig] = len(out)
			out = append(out, t)
		}
	}
	return out
}

// contentSignature identifies logical media independent of its container:
// kind, geometry/timescale and the full sample delta sequence. Provingance and
// container format are intentionally excluded.
func (t *Track) contentSignature() string {
	b := make([]byte, 0, 16+4*len(t.Samples))
	b = append(b, []byte(t.Kind)...)
	b = appendU32(b, uint32(t.Timescale))
	b = appendU16(b, t.Width)
	b = appendU16(b, t.Height)
	b = appendU16(b, t.Channels)
	b = appendU32(b, t.SampleRate)
	for _, s := range t.Samples {
		b = appendU32(b, s.Delta)
	}
	return string(b)
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendU16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}
