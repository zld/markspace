package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"

	"hlsflow/internal/config"
	"hlsflow/internal/integrity"
	"hlsflow/internal/m3u8"
	"hlsflow/internal/safeio"
	"hlsflow/internal/slicer"
	"hlsflow/internal/ts"
)

// builtRendition holds the in-memory outcome of processing one rung before it
// is merged into the cross-rendition report.
type builtRendition struct {
	report   RenditionReport
	playlist m3u8.MediaPlaylist
	// boundaries are output segment start times in seconds on the shared
	// (unwrapped, offset-corrected) timeline, for drift comparison.
	boundaries []float64
	duration   float64
}

// publishFunc atomically republishes the rendition playlist and shared index
// after each source segment, enabling incremental (event-style) publishing.
type publishFunc func(pl m3u8.MediaPlaylist, idx *integrity.Index) error

// processRendition reorders source files, waits on gaps, slices each present
// source segment at IDR boundaries and publishes the resulting files. Missing
// source data always yields a discontinuity, never a silent splice.
func processRendition(
	rd config.Rendition,
	inputRoot, outputRoot *safeio.Root,
	targetTicks int64,
	gapTimeout config.Duration,
	index *integrity.Index,
	publish publishFunc,
) (*builtRendition, []Failure, error) {
	out := &builtRendition{report: RenditionReport{Rendition: rd.Name, Bitrate: rd.Bitrate}}
	var hardFailures []Failure
	targetDuration := int((targetTicks + ts.TicksPerSecond - 1) / ts.TicksPerSecond)
	playlist := m3u8.MediaPlaylist{TargetDuration: targetDuration, MediaSequence: 0}

	inRel := rd.Name // inbox/<name>
	absDir, err := inputRoot.Join(inRel)
	if err != nil {
		return nil, nil, err
	}
	files, _, err := slicer.Discover(absDir)
	if err != nil {
		return nil, nil, err
	}
	present := map[int]string{}
	sortedSeqs := make([]int, 0, len(files))
	for _, f := range files {
		present[f.Seq] = f.Path
		sortedSeqs = append(sortedSeqs, f.Seq)
	}
	// Prefer the encoder's recorded arrival order for reorder accounting.
	arrivalOrder := sortedSeqs
	if manifest, merr := slicer.LoadArrival(absDir); merr != nil {
		return nil, nil, merr
	} else if manifest != nil {
		filtered := make([]int, 0, len(files))
		for _, seq := range manifest.Order {
			if _, ok := present[seq]; ok {
				filtered = append(filtered, seq)
			}
		}
		arrivalOrder = filtered
	}
	out.report.Reordered = slicer.ReorderedCount(arrivalOrder, files)

	if err := outputRoot.MkdirAll(rd.Name); err != nil {
		return nil, nil, err
	}

	mediaSeq := 0
	pendingDiscontinuity := false
	haveCut := false
	var phaseOffset int64 // first IDR PTS: the rendition's timeline phase
	havePhase := false
	streamPos := int64(0) // cumulative unwrapped start tick across source segments

	for seq := rd.FirstSeq; seq <= rd.LastSeq; seq++ {
		path, ok := present[seq]
		if !ok {
			if slicer.WaitForGap(absDir, seq, gapTimeout.Duration) {
				path = filepath.Join(absDir, fmt.Sprintf("seg-%05d.ts", seq))
				ok = true
			}
		}
		if !ok {
			out.report.Missing++
			hardFailures = append(hardFailures, Failure{
				Rendition: rd.Name, Segment: seq, Reason: "missing source segment",
			})
			pendingDiscontinuity = true
			continue
		}
		data, err := readFileOS(path)
		if err != nil {
			return nil, nil, fmt.Errorf("rendition %q seq %d: %w", rd.Name, seq, err)
		}
		parser := ts.NewParser(bytes.NewReader(data))
		cutter := slicer.NewCutter(parser)
		cuts, firstPTS, _, cutErr := cutter.Run(targetTicks)

		// PSI validity is assessed even when the tail was truncated.
		if psiErr := parser.ValidatePSI(); psiErr != nil {
			hardFailures = append(hardFailures, Failure{
				Rendition: rd.Name, Segment: seq, Reason: psiErr.Error(),
			})
		}
		if cutErr != nil {
			reason := cutErr.Error()
			if errors.Is(cutErr, ts.ErrTruncated) {
				reason = "truncated packet at segment tail"
			}
			hardFailures = append(hardFailures, Failure{
				Rendition: rd.Name, Segment: seq, Reason: reason,
			})
		}
		if cutter.PTSWraps > 0 {
			hardFailures = append(hardFailures, Failure{
				Rendition: rd.Name, Segment: seq,
				Reason: fmt.Sprintf("PTS wraparound x%d", cutter.PTSWraps),
			})
		}
		if cutter.PTSBackward > 0 {
			hardFailures = append(hardFailures, Failure{
				Rendition: rd.Name, Segment: seq,
				Reason: fmt.Sprintf("PTS moved backward x%d", cutter.PTSBackward),
			})
		}
		if len(cuts) > 0 && !havePhase {
			phaseOffset = firstPTS
			havePhase = true
		}
		for _, cut := range cuts {
			file := fmt.Sprintf("seg-%05d.ts", mediaSeq)
			rel := filepath.Join(rd.Name, file)
			if err := outputRoot.AtomicWrite(rel, cut.Data); err != nil {
				return nil, nil, err
			}
			// Boundary on the shared timeline: cumulative position plus this
			// rendition's fixed PTS phase offset.
			boundary := streamPos + (cut.StartPTS - firstPTS) + phaseOffset
			boundarySec := ts.Seconds(boundary)
			out.boundaries = append(out.boundaries, boundarySec)
			dur := cut.DurationSeconds()
			index.Add(rd.Name, integrity.Entry{
				Segment: mediaSeq, File: rel,
				SHA256: integrity.HashBytes(cut.Data),
				Size:   int64(len(cut.Data)), Duration: dur,
				BoundarySeconds: round3(boundarySec),
			})
			playlist.Segments = append(playlist.Segments, m3u8.Segment{
				URI: file, Duration: dur,
				Discontinuity: pendingDiscontinuity && haveCut,
				MediaSeq:      mediaSeq,
			})
			streamPos += cut.EndPTS - cut.StartPTS
			out.duration += dur
			mediaSeq++
			haveCut = true
			pendingDiscontinuity = false
		}
		// Incremental, atomic publish after every source segment so a reader
		// never observes a half-written playlist even if the process is killed.
		if publish != nil {
			if err := publish(playlist, index); err != nil {
				return nil, nil, err
			}
		}
	}

	out.playlist = playlist
	out.report.Segments = len(playlist.Segments)
	out.report.DurationSeconds = round3(out.duration)
	return out, hardFailures, nil
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}
