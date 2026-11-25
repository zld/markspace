package demo

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// hlsSegment is one playlist entry with its generated payload.
type hlsSegment struct {
	name        string
	durationMS  int64
	payload     []byte
	discontinuity bool
	byterange   bool
}

// generateHLS writes a media playlist that crosses an EXT-X-DISCONTINUITY
// while keeping one continuous millisecond timeline. Segment payloads are
// deterministic synthetic transport-stream style bytes (not MP4, so they
// exercise the hash/index path without the go-mp4 validator).
func (b *Bundle) generateHLS() error {
	segs := []hlsSegment{
		{name: "seg1.ts", durationMS: 6000, payload: synthPayload("hls-1", 128)},
		{name: "seg2.ts", durationMS: 5500, payload: synthPayload("hls-2", 128)},
		{name: "seg3.ts", durationMS: 4250, payload: synthPayload("hls-3", 128), discontinuity: true},
		{name: "seg4.ts", durationMS: 2000, payload: synthPayload("hls-4", 64), byterange: true},
	}

	var pl strings.Builder
	pl.WriteString("#EXTM3U\n")
	pl.WriteString("#EXT-X-VERSION:3\n")
	pl.WriteString("#EXT-X-TARGETDURATION:6\n")
	pl.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	var cursor int64
	for i, s := range segs {
		if s.discontinuity {
			pl.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		pl.WriteString(fmt.Sprintf("#EXTINF:%s,\n", formatMS(s.durationMS)))
		if s.byterange {
			pl.WriteString(fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d\n", len(s.payload), cursor))
		}
		pl.WriteString(s.name + "\n")
		cursor += int64(len(s.payload))
		_ = i
	}
	pl.WriteString("#EXT-X-ENDLIST\n")

	playlist := []byte(pl.String())
	plPath := filepath.Join(b.Root, DirHLS, M3U8Name)
	if err := writeFile(plPath, playlist); err != nil {
		return err
	}
	b.ManifestHLS = playlist

	var t int64
	for i, s := range segs {
		path := filepath.Join(b.Root, DirHLS, s.name)
		if err := writeFile(path, s.payload); err != nil {
			return err
		}
		b.Segments = append(b.Segments, Segment{
			Path:         path,
			Source:       filepath.Join(DirHLS, s.name),
			RepID:        "hls-0",
			SegmentIndex: int64(i),
			TrackID:      3,
			Timescale:    1000,
			TimeStart:    t,
			TimeEnd:      t + s.durationMS,
			Keyframe:     true,
			ExpectedHash: expectedRecord(s.payload),
			ByteLength:   int64(len(s.payload)),
		})
		t += s.durationMS
	}
	return nil
}

// synthPayload produces deterministic content bytes from a seed, suitable
// for hashing and stable across repeated generator runs.
func synthPayload(seed string, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(int(seed[len(seed)-1]) + i*7)
	}
	// Stamp the seed at the head so distinct seeds never collide.
	copy(out, []byte(seed))
	return out
}

// formatMS renders whole milliseconds as an EXTINF decimal-seconds value
// using integer digit formatting (no floating point).
func formatMS(ms int64) string {
	whole := ms / 1000
	frac := ms % 1000
	if frac == 0 {
		return strconv.FormatInt(whole, 10) + ".0"
	}
	return strconv.FormatInt(whole, 10) + "." + pad3(frac)
}

func pad3(v int64) string {
	s := strconv.FormatInt(v, 10)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
