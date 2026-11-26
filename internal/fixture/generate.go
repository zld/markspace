package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"mediamerge/internal/mp4idx"
)

const sampleCount = 8

// mp4Spec builds the deterministic 8-sample index shared by every scenario.
func mp4Spec(incompleteSync bool) mp4idx.FixtureSpec {
	sizes := make([]uint32, sampleCount)
	deltas := make([]uint32, sampleCount)
	for i := range sizes {
		sizes[i] = 100
		deltas[i] = 3000
	}
	spec := mp4idx.FixtureSpec{
		Timescale: 90000, SampleSizes: sizes, SampleDeltas: deltas,
		SyncSamples: []uint32{1, 2, 3, 4, 5, 6, 7, 8},
	}
	if incompleteSync {
		// No stss box: the index must be rebuilt from the rule that every
		// sample is a sync sample when no sync table is present.
		spec.SyncSamples = nil
	}
	return spec
}

// rtpCapture serializes 8 deterministic RTP packet headers (sequence + 32-bit
// timestamp) as the sideband consumed by internal/rtpclock.
func rtpCapture() []byte {
	// 8 packets, 8 bytes each: uint16 sequence, uint16 reserved, uint32 ts.
	buf := make([]byte, 0, sampleCount*8)
	for i := 0; i < sampleCount; i++ {
		rec := make([]byte, 8)
		binary.BigEndian.PutUint16(rec[0:2], uint16(i))
		binary.BigEndian.PutUint32(rec[4:8], uint32((i+1)*3000))
		buf = append(buf, rec...)
	}
	return buf
}

// mappingDoc is the explicit version/track mapping consumed by the pipeline.
func mappingDoc() []byte {
	return []byte(`{
  "rules": [
    {"source": "subtitles.v1.srt", "version": "v1", "target": "timeline-en", "language": "en", "offset_ticks": 0},
    {"source": "subtitles.v2.vtt", "version": "v2", "target": "timeline-en", "language": "en", "offset_ticks": 0}
  ]
}
`)
}

// Generate writes every file for scenario c under root and returns the bundle.
func Generate(root string, c Case) (*Bundle, error) {
	b := Layout(root, c)
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return nil, err
	}
	damaged := c == CaseDamaged
	conflict := c == CaseConflict

	srt := buildSRT(damaged)
	if damaged {
		// The last healthy cue gets an illegal DF label at 25fps (one
		// rejected cue), while the appended truncated block (built by
		// buildSRT) leaves a coverage gap at its time range.
		srt = injectIllegalDF(srt)
	}
	vtt := buildVTT(damaged, conflict)
	if err := os.WriteFile(b.SRTPath, []byte(srt), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(b.VTTPath, []byte(vtt), 0o644); err != nil {
		return nil, err
	}

	mp4Bytes, err := mp4idx.BuildFixture(mp4Spec(damaged))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(b.MP4Path, mp4Bytes, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(b.RTPPath, rtpCapture(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(b.MappingPath, mappingDoc(), 0o644); err != nil {
		return nil, err
	}

	if damaged {
		b.Damaged = []string{"truncated_cue", "illegal_dropframe_label", "incomplete_sync_index"}
	}
	return b, nil
}

// ExpectedGaps returns the coverage holes a scenario is known to contain.
// The truncated tail block leaves seconds 9..10 uncovered by the SRT source.
func (b *Bundle) ExpectedGaps() []ExpectedGap {
	if len(b.Damaged) == 0 {
		return nil
	}
	return []ExpectedGap{{
		Source:      "subtitles.v1.srt",
		Version:     "v1",
		StartSecond: normalCueCount + 1,
		EndSecond:   normalCueCount + 2,
		Reason:      "truncated cue block leaves the expected range uncovered",
	}}
}

// injectIllegalDF rewrites the last well-formed cue's timestamp line to use
// drop-frame semicolons, which are illegal for the 25fps subtitle rate.
func injectIllegalDF(srt string) string {
	// Replace the timestamp line of cue number normalCueCount.
	target := srtTime(normalCueCount, 0) + " --> " + srtTime(normalCueCount+1, 0)
	repl := "00:00:" + two(normalCueCount) + ";00 --> 00:00:" + two(normalCueCount+1) + ";00"
	return replaceFirst(srt, target, repl)
}

func two(n int) string {
	if n < 10 {
		return "0" + itoaS(n)
	}
	return itoaS(n)
}

func itoaS(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func replaceFirst(s, old, repl string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + repl + s[i+len(old):]
		}
	}
	return s
}

// ensure filepath import stays used even if layout helpers move.
var _ = filepath.Join
