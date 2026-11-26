// Package fixture generates all sample inputs the project consumes, so no
// external media files are referenced. Outputs are deterministic: the same
// case and directory always produce byte-identical files and hashes.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/media"
)

// Case names accepted by the `fixture --case` flag.
const (
	CaseOK             = "ok"
	CaseTruncatedMdat  = "truncated-mdat"
	CaseDuplicateDASH  = "duplicate-dash"
	CaseHLSGap         = "hls-discontinuity-gap"
	CaseRTPWrap        = "rtp-wrap"
)

// Frame dimensions shared by every grayscale sample frame.
const (
	FrameW = 32
	FrameH = 24
)

// Bandwidth targets for the three DASH representations.
var bandwidths = []struct {
	id  string
	bps int64
}{
	{"v400", 400_000},
	{"v800", 800_000},
	{"v1200", 1_200_000},
}

const (
	segmentsPerRep = 8
	segmentSecs    = 2
	timescale      = 48000
	sampleDuration = timescale * segmentSecs // 96000 ticks = 2 s
)

// Generator writes a complete fixture tree.
type Generator struct {
	Root string
}

// New creates the output directory.
func New(root string) (*Generator, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, clock.New(clock.KindIO, "fixture", "mkdir: %v", err)
	}
	return &Generator{Root: root}, nil
}

func (g *Generator) write(rel string, data []byte) error {
	full := filepath.Join(g.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// frameFor returns the deterministic grayscale frame for a segment index.
// Even indices are the clean gradient; odd indices are a bounded-noise
// variant used for the overlap PSNR acceptance.
func frameFor(idx int) *media.GrayFrame {
	base := media.GradientFrame(FrameW, FrameH)
	if idx%2 == 0 {
		return base
	}
	return media.NoisyFrame(base, 3, 1000+idx)
}

// buildFragment renders one padded CMAF fragment for a target bitrate.
func (g *Generator) buildFragment(repID string, idx int, bps int64) ([]byte, error) {
	frame := frameFor(idx)
	w := &media.CMAFWriter{
		Sequence:   uint32(idx + 1),
		Timescale:  timescale,
		TrackID:    1,
		BaseDecode: uint64(idx * sampleDuration),
	}
	frag, err := w.Fragment(sampleDuration, frame.Pix)
	if err != nil {
		return nil, err
	}
	target := media.TargetBytesForBitrate(bps, segmentSecs)
	if target > len(frag) {
		frag = media.PadToTargetSize(frag, target)
	}
	return frag, nil
}

// Generate writes the full "ok" tree: primary + backup DASH (3 reps),
// HLS playlists with a discontinuity, grayscale reference frames and the
// source-switch record.
func (g *Generator) Generate() error {
	if err := g.writeSwitchRecords(); err != nil {
		return err
	}
	if err := g.writeReferenceFrames(); err != nil {
		return err
	}
	if err := g.writeDASH(SourcePrimary, false); err != nil {
		return err
	}
	if err := g.writeDASH(SourceBackup, true); err != nil {
		return err
	}
	if err := g.writeHLS(); err != nil {
		return err
	}
	return g.writeManifestIndex()
}

// Source directory labels.
const (
	SourcePrimary = "primary"
	SourceBackup  = "backup"
)

// writeDASH emits one MPD plus media for all three representations. When
// backup is false, segment index 3 is omitted from both disk and the
// SegmentTimeline, so the primary timeline carries a real [6s,8s) gap;
// the backup carries full continuity so the gap can be recovered.
func (g *Generator) writeDASH(source string, isBackup bool) error {
	base := filepath.Join("dash", source)
	for _, bw := range bandwidths {
		number := 0
		for idx := 0; idx < segmentsPerRep; idx++ {
			if !isBackup && idx == 3 {
				continue // primary gap on the common timeline [6s,8s)
			}
			number++
			frag, err := g.buildFragment(bw.id, idx, bw.bps)
			if err != nil {
				return err
			}
			rel := filepath.Join(base, bw.id, fmt.Sprintf("seg-%d.m4s", number))
			if err := g.write(rel, frag); err != nil {
				return err
			}
		}
	}
	mpd := renderMPD(base, isBackup)
	return g.write(filepath.Join(base, "prog.mpd"), []byte(mpd))
}

func renderMPD(base string, isBackup bool) string {
	var reps string
	for _, bw := range bandwidths {
		reps += fmt.Sprintf(
			"      <Representation id=%q bandwidth=\"%d\"/>\n", bw.id, bw.bps)
	}
	var sEntries string
	num := 0
	for idx := 0; idx < segmentsPerRep; idx++ {
		if !isBackup && idx == 3 {
			continue // timeline skips this interval, leaving a gap
		}
		num++
		t := idx * sampleDuration
		sEntries += fmt.Sprintf("          <S t=\"%d\" d=\"%d\"/>\n", t, sampleDuration)
	}
	_ = num
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT%dS">
  <Period>
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="%d" media="$RepresentationID$/seg-$Number$.m4s"
        initialization="$RepresentationID$/init.mp4" startNumber="1">
        <SegmentTimeline>
%s        </SegmentTimeline>
      </SegmentTemplate>
%s    </AdaptationSet>
  </Period>
</MPD>
`, segmentsPerRep*segmentSecs, timescale, sEntries, reps)
}
