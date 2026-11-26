package pipeline

import (
	"os"
	"path/filepath"

	"mediamerge/internal/clock"
	"mediamerge/internal/fixture"
	"mediamerge/internal/mapping"
	"mediamerge/internal/merge"
	"mediamerge/internal/mp4idx"
	"mediamerge/internal/rtpclock"
	"mediamerge/internal/subtitle"
	"mediamerge/internal/track"
)

func parseSRT(b *fixture.Bundle, lenient bool) (*track.Track, []subtitle.RejectedCue, error) {
	f, err := os.Open(b.SRTPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	name := filepath.Base(b.SRTPath)
	if !lenient {
		tr, err := subtitle.ParseSRT(name, "v1", f)
		return tr, nil, err
	}
	return subtitle.ParseSRTWithOptions(name, "v1", f,
		subtitle.Options{Lenient: true, Rate: defaultRate()})
}

func parseVTT(b *fixture.Bundle, lenient bool) (*track.Track, []subtitle.RejectedCue, error) {
	f, err := os.Open(b.VTTPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	name := filepath.Base(b.VTTPath)
	if !lenient {
		tr, err := subtitle.ParseVTT(name, "v2", f)
		return tr, nil, err
	}
	return subtitle.ParseVTTWithOptions(name, "v2", f,
		subtitle.Options{Lenient: true, Rate: defaultRate()})
}

func defaultRate() clock.Rate { return clock.Rate25 }

func mapTracks(table *mapping.Table, srtTrack, vttTrack *track.Track) ([]merge.MappedTrack, error) {
	var out []merge.MappedTrack
	for _, tr := range []*track.Track{srtTrack, vttTrack} {
		rule, err := table.Lookup(tr.Source, tr.Version)
		if err != nil {
			return nil, err
		}
		mapped, err := mapping.Apply(rule, tr)
		if err != nil {
			return nil, err
		}
		out = append(out, merge.MappedTrack{
			SourceID: tr.Source, Version: tr.Version, TargetID: rule.TargetTrack,
			Language: rule.Language, Track: mapped,
		})
	}
	return out, nil
}

func keyframeStats(b *fixture.Bundle) (hits, total int, err error) {
	f, err := os.Open(b.MP4Path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	idx, err := mp4idx.NewReader(b.MP4Path, f).Read()
	if err != nil {
		return 0, 0, err
	}
	for _, s := range idx.Samples {
		total++
		if s.IsSync && s.SyncKnown {
			hits++
		}
	}
	return hits, total, nil
}

func checkRTP(b *fixture.Bundle) error {
	f, err := os.Open(b.RTPPath)
	if err != nil {
		return err
	}
	defer f.Close()
	events, err := fixture.ReadRTPStream(b.RTPPath, f, 90000)
	if err != nil {
		return err
	}
	for _, ev := range events {
		switch ev.Kind {
		case rtpclock.EventDuplicate, rtpclock.EventReordered,
			rtpclock.EventLost, rtpclock.EventWrap:
			// Anomalies are valid sideband classifications, not hard errors.
		}
	}
	return nil
}

// parseTrack parses a subtitle file in the strict form used during recovery.
func parseTrack(path, version, format string, f *os.File) (*track.Track, []subtitle.RejectedCue, error) {
	switch format {
	case "srt":
		tr, err := subtitle.ParseSRT(path, version, f)
		return tr, nil, err
	default:
		tr, err := subtitle.ParseVTT(path, version, f)
		return tr, nil, err
	}
}
