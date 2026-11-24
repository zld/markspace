// Package fixture generates the sample and corrupt media fixtures used
// by the CLI and the tests. Everything is generated in Go: two DASH
// representations, an HLS playlist with a discontinuity, and the
// boundary cases (truncated box header, zero timescale, duplicate
// timestamps, cross-discontinuity jump).
package fixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	// Timescale of the generated fMP4 tracks.
	Timescale = 90000
	// SampleDur is the sample duration in track units (1/30 s).
	SampleDur = 3000
	// SamplesPerSeg is the number of samples per media segment (2 s).
	SamplesPerSeg = 60
	// SegmentsPerRep is the number of DASH segments per representation.
	SegmentsPerRep = 5
)

// Rep describes one generated DASH representation.
type Rep struct {
	ID         string
	Bandwidth  int64
	SampleSize int // bytes per sample, sized to hit the bandwidth
}

// Reps are the two generated representations: v1 at ~1.2 Mbps and v2 at
// ~400 kbps.
var Reps = []Rep{
	{ID: "v1", Bandwidth: 1200000, SampleSize: 5000},
	{ID: "v2", Bandwidth: 400000, SampleSize: 1666},
}

// Generate writes the full fixture tree into dir.
func Generate(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, rep := range Reps {
		if err := writeInit(dir, rep); err != nil {
			return err
		}
		for seg := 1; seg <= SegmentsPerRep; seg++ {
			if err := writeSegment(dir, rep, seg); err != nil {
				return err
			}
		}
	}
	if err := writeMPD(dir); err != nil {
		return err
	}
	if err := writeHLS(dir); err != nil {
		return err
	}
	return GenerateCorrupt(dir)
}

// writeInit writes the initialization segment of one representation.
func writeInit(dir string, rep Rep) error {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(Timescale, "video", "und")
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("init-%s.mp4", rep.ID)), buf.Bytes(), 0o644)
}

// writeSegment writes one media segment. Segment 4 of v1 repeats the
// previous segment's tfdt to exercise duplicate-timestamp repair.
func writeSegment(dir string, rep Rep, seq int) error {
	base := uint64(seq-1) * uint64(SamplesPerSeg) * SampleDur
	if rep.ID == "v1" && seq == 4 {
		base = uint64(4-2) * uint64(SamplesPerSeg) * SampleDur // duplicate of seg 3
	}
	data := EncodeSegment(uint32(seq), base, rep.SampleSize)
	name := fmt.Sprintf("seg-%s-%d.m4s", rep.ID, seq)
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

// EncodeSegment builds one media segment: SamplesPerSeg samples, the
// first sample of each segment a sync sample.
func EncodeSegment(seq uint32, baseDecodeTime uint64, sampleSize int) []byte {
	frag, err := mp4.CreateFragment(seq, 1)
	if err != nil {
		panic(err)
	}
	for i := 0; i < SamplesPerSeg; i++ {
		flags := uint32(mp4.NonSyncSampleFlags)
		if i == 0 {
			flags = mp4.SyncSampleFlags
		}
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.NewSample(flags, SampleDur, uint32(sampleSize), 0),
			DecodeTime: baseDecodeTime + uint64(i)*SampleDur,
			Data:       bytes.Repeat([]byte{byte(i)}, sampleSize),
		})
	}
	var buf bytes.Buffer
	if err := frag.Encode(&buf); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// writeMPD writes the two-representation DASH manifest.
func writeMPD(dir string) error {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>
<MPD>
  <Period>
    <AdaptationSet>
`)
	for _, rep := range Reps {
		fmt.Fprintf(&b, `      <Representation id="%s" bandwidth="%d">
        <SegmentTemplate timescale="%d" initialization="init-$RepresentationID$.mp4"
            media="seg-$RepresentationID$-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="180000" r="%d"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
`, rep.ID, rep.Bandwidth, Timescale, SegmentsPerRep-1)
	}
	b.WriteString(`    </AdaptationSet>
  </Period>
</MPD>
`)
	return os.WriteFile(filepath.Join(dir, "manifest.mpd"), b.Bytes(), 0o644)
}
