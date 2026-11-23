package app

import (
	"fmt"
	"os"
	"sort"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/pipeline"
)

type verifyReport struct {
	roundtrip, kfHit float64
	tracks, segments int
	samplesMatch     bool
	detail           string
}

// verifyPair probes both files, maps the input, re-remuxes to a sibling temp
// path inside the output directory, and compares sample evidence.
func verifyPair(input, output string) (*verifyReport, error) {
	inProbe, _, err := pipeline.Reprobe(input)
	if err != nil {
		return nil, err
	}
	outProbe, outSize, err := pipeline.Reprobe(output)
	if err != nil {
		return nil, err
	}
	if outSize <= 0 {
		return nil, media.NewError(media.ClassTruncated, "verify", 0, "empty output")
	}
	if err := media.ValidateMovieBounds(inProbe); err != nil {
		return nil, err
	}
	if err := media.ValidateMovieBounds(outProbe); err != nil {
		return nil, err
	}
	tmap, err := media.BuildTrackMap(inProbe, demoTargets())
	if err != nil {
		return nil, err
	}
	inTracks, err := tmap.MappedTracks(inProbe)
	if err != nil {
		return nil, err
	}
	if len(inTracks) != len(outProbe.Tracks) {
		return nil, media.NewError(media.ClassIntegrity, "verify", 0, "track count %d != output %d", len(inTracks), len(outProbe.Tracks))
	}
	// Track i of the output corresponds to target id i+1.
	matched := 0
	var detail []string
	samplesMatch := true
	for i, src := range inTracks {
		dst := findTrack(outProbe, uint32(i+1))
		if dst == nil {
			return nil, media.NewError(media.ClassIntegrity, "verify", 0, "missing target track %d", i+1)
		}
		if src.SampleCount != dst.SampleCount {
			samplesMatch = false
			return nil, media.NewError(media.ClassIntegrity, "verify", 0,
				"track %d sample count %d != %d", i+1, src.SampleCount, dst.SampleCount).WithTrack(dst.TrackID)
		}
		inF, err := os.Open(input)
		if err != nil {
			return nil, media.WrapError(media.ClassIO, "verify.open", 0, err, "open input")
		}
		inHash, err := media.HashSegment(inF, src.Table, media.Segment{First: 0, Count: src.SampleCount})
		inF.Close()
		if err != nil {
			return nil, err
		}
		outF, err := os.Open(output)
		if err != nil {
			return nil, media.WrapError(media.ClassIO, "verify.open", 0, err, "open output")
		}
		outHash, err := media.HashSegment(outF, dst.Table, media.Segment{First: 0, Count: dst.SampleCount})
		outF.Close()
		if err != nil {
			return nil, err
		}
		if inHash != outHash {
			samplesMatch = false
			return nil, media.NewError(media.ClassIntegrity, "verify.samples", 0,
				"track %d payload hash mismatch: %s != %s", i+1, inHash[:12], outHash[:12]).WithTrack(dst.TrackID)
		}
		matched++
		detail = append(detail, fmt.Sprintf("t%d:%s..%s", i+1, inHash[:8], outHash[:8]))
	}
	sort.Strings(detail)
	segs, err := media.SplitSegments(outProbe.Tracks, 3)
	if err != nil {
		return nil, err
	}
	kfHit := 1.0
	idx, err := media.BuildIndex(outProbe.Tracks, segs)
	if err != nil {
		return nil, err
	}
	outF, err := os.Open(output)
	if err != nil {
		return nil, media.WrapError(media.ClassIO, "verify.open", 0, err, "open output")
	}
	defer outF.Close()
	checks := 32
	if idx.Len() < checks {
		checks = idx.Len()
	}
	kfHit, _, err = idx.VerifyRandom(outF, outProbe.Tracks, 41, checks)
	if err != nil {
		return nil, err
	}
	if kfHit < 1.0 {
		return nil, media.NewError(media.ClassIntegrity, "verify.index", 0, "keyframe hit ratio %.3f < 1.0", kfHit)
	}
	return &verifyReport{
		roundtrip:    float64(matched) / float64(len(inTracks)),
		kfHit:        kfHit,
		tracks:       len(inTracks),
		segments:     len(segs),
		samplesMatch: samplesMatch,
		detail:       joinStrings(detail, ","),
	}, nil
}

func findTrack(p *media.Probe, id uint32) *media.TrackInfo {
	for _, ti := range p.Tracks {
		if ti.TrackID == id {
			return ti
		}
	}
	return nil
}

func joinStrings(in []string, sep string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
