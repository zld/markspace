package pipeline

import (
	"mediarun/internal/clock"
	"mediarun/internal/parse"
)

// hlsWindow tracks one discontinuity window's mapping base: the first raw
// media PTS of the window maps to the declared playlist start of that
// segment, so a PTS reset at a discontinuity never moves the play cursor
// backwards.
type hlsWindow struct {
	basePTS  clock.Ticks // first raw PTS of the window
	baseDecl clock.Ticks // declared timeline start of that segment
}

// processHLS handles one HLS playlist, absorbing PTS jumps at
// discontinuities while keeping the play timeline monotonic.
func processHLS(inDir string, pl HLSTimeline) (*TrackResult, error) {
	tr := &TrackResult{ID: "hls:" + pl.Playlist, Kind: "hls", Timescale: clock.MasterTimescale}
	windows := map[int]hlsWindow{}
	var trackEnd clock.Ticks
	for i, seg := range pl.Segments {
		data, digest, err := readSegment(inDir, seg.URI)
		if err != nil {
			return nil, err
		}
		info, err := parse.ParseTS(seg.URI, data)
		if err != nil {
			return nil, err
		}
		w, ok := windows[seg.DiscSeq]
		if !ok {
			w = hlsWindow{basePTS: info.FirstPTS, baseDecl: seg.DeclaredStart}
			windows[seg.DiscSeq] = w
		}
		actual := w.baseDecl + (info.FirstPTS - w.basePTS)
		measured := info.LastPTS - info.FirstPTS
		sr := SegmentResult{
			Path:             seg.URI,
			Digest:           digest,
			DeclaredStart:    seg.DeclaredStart,
			ActualStart:      actual,
			Duration:         measured,
			DiscontinuitySeq: seg.DiscSeq,
			SyncPoints: []SyncPoint{{
				Tick: actual, MoofOffset: 0, SampleIndex: i, DataOffset: 0,
			}},
		}
		if !ok && seg.DiscSeq > 0 {
			// Report the mapping shift introduced at the discontinuity.
			sr.DiscontinuityOff = w.baseDecl - w.basePTS
		}
		sr.AlignmentErrorMs = absMillis(sr.DeclaredStart - sr.ActualStart)
		if end := actual + measured; end > trackEnd {
			trackEnd = end
		}
		tr.MediaBytes += info.PayloadBytes
		tr.SampleCount += info.Packets
		tr.Segments = append(tr.Segments, sr)
	}
	tr.Duration = trackEnd
	return tr, nil
}
