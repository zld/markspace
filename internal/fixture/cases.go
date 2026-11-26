package fixture

import (
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/media"
)

// writeHLS writes primary and backup media playlists. The primary playlist
// skips one segment interval around a discontinuity; the backup covers it.
func (g *Generator) writeHLS() error {
	// Backup: fully continuous, one variant with a discontinuity marker at
	// segment index 4 (the cut point).
	if err := g.writeHLSVariant(SourceBackup, true); err != nil {
		return err
	}
	// Primary: same media except index 3 is absent, creating a gap.
	return g.writeHLSVariant(SourcePrimary, false)
}

func (g *Generator) writeHLSVariant(source string, isBackup bool) error {
	dir := filepath.Join("hls", source)
	var pl string
	pl += "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:2\n"
	pl += "#EXT-X-MEDIA-SEQUENCE:0\n"
	number := 0
	for idx := 0; idx < segmentsPerRep; idx++ {
		if !isBackup && idx == 3 {
			continue
		}
		if idx == 4 {
			pl += "#EXT-X-DISCONTINUITY\n"
		}
		frag, err := g.buildFragment("hls", idx, bandwidths[1].bps)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("seg-%d.m4s", number)
		if err := g.write(filepath.Join(dir, name), frag); err != nil {
			return err
		}
		pl += fmt.Sprintf("#EXTINF:%d.0,\n%s\n", segmentSecs, name)
		number++
	}
	pl += "#EXT-X-ENDLIST\n"
	return g.write(filepath.Join(dir, "pl.m3u8"), []byte(pl))
}

// writeReferenceFrames stores the clean gradient used for overlap PSNR.
func (g *Generator) writeReferenceFrames() error {
	ref := media.GradientFrame(FrameW, FrameH)
	// A small sidecar records frame geometry so the verifier needs no
	// out-of-band configuration.
	meta := fmt.Sprintf("width=%d\nheight=%d\n", FrameW, FrameH)
	if err := g.write(filepath.Join("frames", "ref.gray"), ref.Pix); err != nil {
		return err
	}
	return g.write(filepath.Join("frames", "meta.txt"), []byte(meta))
}

// writeSwitchRecords emits a JSONL log including a 32-bit RTP timestamp
// wrap between the primary and backup cut records.
func (g *Generator) writeSwitchRecords() error {
	// 48 kHz clock: primary ends near the uint32 ceiling, backup continues
	// past the wrap, exercising UnwrapTimestamp.
	const rate = 48000
	recs := []struct {
		source    string
		seq       uint16
		ts        uint32
		programNS int64
	}{
		{"primary", 100, 0xFFFFFFF0, 6_000_000_000},
		{"backup", 101, 0x00000040, 8_000_000_000},
		{"primary", 200, 0x00000090, 10_000_000_000},
	}
	var out string
	for _, r := range recs {
		out += fmt.Sprintf(
			`{"source":%q,"seq":%d,"timestamp":%d,"clock_rate":%d,"program_ns":%d}`+"\n",
			r.source, r.seq, r.ts, rate, r.programNS)
	}
	return g.write(filepath.Join("switch", "records.jsonl"), []byte(out))
}

// writeManifestIndex is the entry descriptor the CLI reads to locate the
// primary/backup manifests and the switch log.
func (g *Generator) writeManifestIndex() error {
	doc := `{
  "dash": {
    "primary": "dash/primary/prog.mpd",
    "backup": "dash/backup/prog.mpd"
  },
  "hls": {
    "primary": "hls/primary/pl.m3u8",
    "backup": "hls/backup/pl.m3u8"
  },
  "switch_records": "switch/records.jsonl",
  "frames": {
    "reference": "frames/ref.gray",
    "meta": "frames/meta.txt"
  }
}
`
	return g.write("index.json", []byte(doc))
}

// GenerateBad writes a named failure fixture.
func (g *Generator) GenerateBad(kind string) error {
	switch kind {
	case CaseTruncatedMdat:
		return g.badTruncatedMdat()
	case CaseDuplicateDASH:
		return g.badDuplicateDASH()
	case CaseHLSGap:
		return g.badHLSUnfillableGap()
	case CaseRTPWrap:
		// The wrap record is already part of the ok tree; this case writes
		// only the switch log for focused RTP tests.
		return g.writeSwitchRecords()
	default:
		return clock.New(clock.KindInvalidInput, "fixture",
			"unknown bad case %q", kind)
	}
}

// badTruncatedMdat builds an otherwise-valid tree but truncates one mdat,
// which media inspection must reject before anything is published.
func (g *Generator) badTruncatedMdat() error {
	if err := g.Generate(); err != nil {
		return err
	}
	target := filepath.Join(g.Root, "dash", "primary", "v400", "seg-1.m4s")
	raw, err := os.ReadFile(target)
	if err != nil {
		return clock.New(clock.KindIO, "fixture", "read for truncation: %v", err)
	}
	if len(raw) < 12 {
		return clock.New(clock.KindInvalidInput, "fixture", "fragment too small")
	}
	// Drop the last 5 bytes so the mdat length header overshoots EOF.
	return os.WriteFile(target, raw[:len(raw)-5], 0o644)
}

// badDuplicateDASH writes a primary MPD whose SegmentTimeline resolves two
// entries to the same media URL, a parser-level duplicate reference.
func (g *Generator) badDuplicateDASH() error {
	if err := g.writeSwitchRecords(); err != nil {
		return err
	}
	base := filepath.Join("dash", SourcePrimary)
	if err := g.write(filepath.Join(base, "v", "seg-0.m4s"), []byte("x")); err != nil {
		return err
	}
	mpd := `<?xml version="1.0"?>
<MPD mediaPresentationDuration="PT4S"><Period><AdaptationSet>
  <SegmentTemplate timescale="1000" media="v/seg-$Time$.m4s" startNumber="1">
    <SegmentTimeline>
      <S t="0" d="2000"/>
      <S t="0" d="2000"/>
    </SegmentTimeline>
  </SegmentTemplate>
  <Representation id="v" bandwidth="100000"/>
</AdaptationSet></Period></MPD>`
	return g.write(filepath.Join(base, "prog.mpd"), []byte(mpd))
}

// badHLSUnfillableGap writes a primary playlist with a discontinuity gap and
// no backup segment covering the missing interval.
func (g *Generator) badHLSUnfillableGap() error {
	dir := filepath.Join("hls", SourcePrimary)
	// Only two disjoint segments with a discontinuity and nothing in
	// between, and no backup directory at all.
	frag, err := g.buildFragment("hls", 0, bandwidths[1].bps)
	if err != nil {
		return err
	}
	if err := g.write(filepath.Join(dir, "seg-0.m4s"), frag); err != nil {
		return err
	}
	frag2, err := g.buildFragment("hls", 5, bandwidths[1].bps)
	if err != nil {
		return err
	}
	if err := g.write(filepath.Join(dir, "seg-1.m4s"), frag2); err != nil {
		return err
	}
	pl := "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:2\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:2.0,\nseg-0.m4s\n" +
		"#EXT-X-DISCONTINUITY\n" +
		"#EXTINF:2.0,\nseg-1.m4s\n#EXT-X-ENDLIST\n"
	return g.write(filepath.Join(dir, "pl.m3u8"), []byte(pl))
}
