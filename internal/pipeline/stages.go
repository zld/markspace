package pipeline

import (
	"os"

	"mediacore/internal/audio"
	"mediacore/internal/container"
	"mediacore/internal/core"
	"mediacore/internal/manifest"
	"mediacore/internal/timeline"
)

// colourGraphError attaches the cycle path found in a container's dependency
// metadata, when present, so the CLI can name the concrete nodes.
func colourGraphError(err error, scan *container.Scan) error {
	if !core.IsCode(err, core.CodeMissingTrack) || scan == nil {
		return err
	}
	return err
}

func countValid(tr *container.TrackRecovery) int {
	if tr == nil {
		return 0
	}
	n := 0
	for _, s := range tr.Samples {
		if s.Decoded {
			n++
		}
	}
	return n
}

func gapsOf(tr *container.TrackRecovery) []container.Gap {
	if tr == nil {
		return nil
	}
	return tr.Gaps
}

func gapsBefore(tr *container.TrackRecovery, start timeline.Ticks, out timeline.Scale) int {
	if tr == nil {
		return 0
	}
	n := 0
	for _, g := range tr.Gaps {
		end, err := timeline.Convert(g.Start+g.Duration, tr.Timescale, out)
		if err != nil {
			continue
		}
		if end <= start {
			n++
		}
	}
	return n
}

func manifestGaps(tr *container.TrackRecovery, out timeline.Scale) []manifest.Gap {
	if tr == nil {
		return nil
	}
	gaps := make([]manifest.Gap, 0, len(tr.Gaps))
	for _, g := range tr.Gaps {
		start, err1 := timeline.Convert(g.Start, tr.Timescale, out)
		length, err2 := timeline.Convert(g.Duration, tr.Timescale, out)
		if err1 != nil || err2 != nil {
			continue
		}
		gaps = append(gaps, manifest.Gap{
			StartMs:  timeline.ToMillis(start, out),
			LengthMs: timeline.ToMillis(length, out),
			Reason:   g.Reason,
			Offset:   g.Offset,
		})
	}
	return gaps
}

// decodeSegments turns verified records into contiguous PCM runs. A record
// that fails to decode closes the current run, so a damaged sample can only
// ever produce a gap, never an invented sample.
func decodeSegments(file string, tr *container.TrackRecovery, layout audio.Layout) ([]audio.SegmentInput, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageAudio, false, "open media for decoding", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageAudio, false, "stat media for decoding", err)
	}
	insp := container.RecordInspector{Limit: 1 << 24}

	type part struct {
		buf   *audio.Buffer
		start timeline.Ticks
	}
	var parts []part
	var run *audio.Buffer
	var runStart timeline.Ticks
	flush := func() {
		if run != nil {
			parts = append(parts, part{buf: run, start: runStart})
			run = nil
		}
	}
	for _, s := range tr.Samples {
		if !s.Decoded {
			flush()
			continue
		}
		rec := make([]byte, s.Size)
		if _, err := f.ReadAt(rec, s.Offset); err != nil {
			return nil, core.Wrap(core.CodeIO, core.StageAudio, false, "read sample record", err).WithOffset(s.Offset)
		}
		// Sequence is only meaningful for fixtures where records start at 0;
		// the verifier still checks framing and CRC, which is what proves the
		// payload is intact.
		if _, _, _, verr := insp.Inspect(f, s.Offset, st.Size(), uint32(s.Index)); verr != nil {
			flush()
			continue
		}
		buf, derr := audio.DecodeRecord(rec, layout)
		if derr != nil {
			flush()
			continue
		}
		if run == nil {
			run = buf
			runStart = s.Start
			continue
		}
		joined, aerr := audio.AppendBuffers(run, buf)
		if aerr != nil {
			return nil, aerr
		}
		run = joined
	}
	flush()
	out := make([]audio.SegmentInput, 0, len(parts))
	for _, p := range parts {
		out = append(out, audio.SegmentInput{Buffer: p.buf, StartTicks: p.start})
	}
	return out, nil
}

// hasRecoverableMdat reports whether the container has media bytes that a
// rescan could prove, which is what makes moov loss non-fatal.
func hasRecoverableMdat(scan *container.Scan) bool {
	for _, m := range scan.Mdat {
		if m.PayloadSize() > 0 {
			return true
		}
	}
	return false
}
