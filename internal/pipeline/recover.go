package pipeline

import (
	"os"
	"time"

	"mediapipeline/internal/demux"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/probe"
	"mediapipeline/internal/remux"
	"mediapipeline/internal/track"
)

// RecoverySummary reports one recovery run.
type RecoverySummary struct {
	Format            string         `json:"format"`
	Tracks            int            `json:"tracks"`
	EventsIn          int            `json:"events_in"`
	EventsOut         int            `json:"events_out"`
	EventOrderSHA256  string         `json:"event_order_sha256"`
	PayloadSHA256In   string         `json:"payload_sha256_in"`
	PayloadSHA256Out  string         `json:"payload_sha256_out"`
	Committed         bool           `json:"committed"`
	ResumeMS          int64          `json:"resume_ms"`
	CommittedSegments int            `json:"committed_segments"`
	RecoveredSegments int            `json:"recovered_segments"`
	DuplicateSegments int            `json:"duplicate_segments"`
	Incomplete        []demux.Range  `json:"incomplete,omitempty"`
	Segments          []SegmentState `json:"segments,omitempty"`
	Resumed           bool           `json:"resumed"`
	Reason            string         `json:"reason,omitempty"`
	ErrorStage        string         `json:"error_stage,omitempty"`
	InputOffset       int64          `json:"input_offset,omitempty"`
}

// Recover rebuilds a fragmented MP4 that has no usable index by scanning
// moof/mdat pairs sequentially.  It keeps only fully verified fragments, marks
// the incomplete range, and records a checkpoint so a repeated run reports the
// already committed segments instead of writing them again.
func Recover(workDir, inputPath, outputPath string) (RecoverySummary, error) {
	start := time.Now()
	sum := RecoverySummary{}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return sum, err
	}
	res, err := probe.Detect(data)
	if err != nil {
		return failRecovery(sum, err), err
	}
	sum.Format = string(res.Format)

	// Reuse the verified checkpoint when the input is unchanged and the
	// committed output already matches it: segment payloads must not be
	// written a second time.
	cp, hasCP := loadRecoveryCheckpoint(workDir)
	scan, err := demux.ScanFragments(data, 0, demux.Options{})
	if err != nil {
		return failRecovery(sum, err), err
	}
	sum.Incomplete = scan.Incomplete
	sum.RecoveredSegments = len(scan.Segments)

	states := segmentStates(scan)
	sum.DuplicateSegments = countDuplicates(states)
	sum.Segments = states
	if hasCP && cp.InputSHA256 == sha256Hex(data) && containsAll(cp.Segments, states) {
		if existing, ok := committedOutputMatches(outputPath, cp.PayloadSHA256); ok {
			sum.Resumed = true
			sum.Committed = true
			sum.CommittedSegments = len(cp.Segments)
			sum.PayloadSHA256In = cp.PayloadSHA256In
			sum.PayloadSHA256Out = existing
			sum.EventsIn = cp.EventsIn
			sum.EventsOut = cp.EventsOut
			sum.EventOrderSHA256 = cp.EventOrderSHA256
			sum.Tracks = len(cp.Tracks)
			sum.ResumeMS = time.Since(start).Milliseconds()
			return sum, nil
		}
	}

	media, err := demux.ParseMP4(data, demux.Options{
		SampleEvents: true, AllowMissingIndex: true, TolerateTruncated: true,
	})
	if err != nil {
		return failRecovery(sum, err), err
	}
	for ti := range media.Tracks {
		media.Tracks[ti].Samples = media.Tracks[ti].Samples[:0]
	}
	if err := attachRecoveredSamples(media, scan); err != nil {
		return failRecovery(sum, err), err
	}
	sum.Tracks = len(media.Tracks)
	sum.EventsIn = len(media.Events)

	merged, err := metadata.Merge(media.Events)
	if err != nil {
		return failRecovery(sum, fault.Wrap(fault.StageMetadata, sum.Format, fault.OpEvent, -1, "merge events", err)), err
	}
	sum.EventsOut = len(merged.Events)
	sum.EventOrderSHA256 = merged.Fingerprint()

	inputs := trackInputs(media.Tracks)
	mapping, err := track.Identity(inputs)
	if err != nil {
		return failRecovery(sum, fault.Wrap(fault.StageMap, sum.Format, fault.OpMapping, -1, "build mapping", err)), err
	}
	sum.PayloadSHA256In = MediaPayloadDigest(media)

	tmp, err := stage(workDir, outputPath, StageRemux, func(tmp string) error {
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out, err := remux.Write(f, remux.Plan{
			Src: data, Tracks: media.Tracks, Mapping: mapping,
			Events: merged, MajorBrand: media.MajorBrand, Cut: media.Cut,
		})
		if err != nil {
			return err
		}
		sum.PayloadSHA256Out = out.PayloadSHA256
		return nil
	})
	if err != nil {
		return failRecovery(sum, err), err
	}
	if err := VerifyFile(tmp, Verification{
		ExpectTracks: len(media.Tracks), ExpectEvents: len(merged.Events),
		PayloadSHA256: sum.PayloadSHA256In, EventOrderSHA256: sum.EventOrderSHA256,
		Format: sum.Format,
	}); err != nil {
		_ = os.Remove(tmp)
		return failRecovery(sum, err), err
	}
	if err := os.Rename(tmp, outputPath); err != nil {
		_ = os.Remove(tmp)
		return failRecovery(sum, err), err
	}
	sum.Committed = true
	sum.CommittedSegments = len(states)
	sum.ResumeMS = time.Since(start).Milliseconds()

	newCP := &RecoveryCheckpoint{
		Input:            inputPath,
		InputSHA256:      sha256Hex(data),
		PayloadSHA256In:  sum.PayloadSHA256In,
		PayloadSHA256:    sum.PayloadSHA256Out,
		EventsIn:         sum.EventsIn,
		EventsOut:        sum.EventsOut,
		EventOrderSHA256: sum.EventOrderSHA256,
		Segments:         states,
		Tracks:           mapping.Entries,
	}
	if err := writeRecoveryCheckpoint(workDir, newCP); err != nil {
		return failRecovery(sum, err), err
	}
	return sum, nil
}

// attachRecoveredSamples walks complete segments in input order and appends
// each track's samples with absolute source offsets.  The timescale comes from
// the moov, so recovered times match a normal demux exactly.
func attachRecoveredSamples(media *demux.Media, scan *demux.FragmentScan) error {
	byID := make(map[uint32]*demux.Track, len(media.Tracks))
	for i := range media.Tracks {
		byID[media.Tracks[i].ID] = &media.Tracks[i]
	}
	for _, seg := range scan.Segments {
		layout, err := demux.ScanSegment(seg, media.Tracks)
		if err != nil {
			return err
		}
		for _, s := range layout {
			tr, ok := byID[s.TrackID]
			if !ok {
				continue
			}
			tr.Samples = append(tr.Samples, s.Sample)
		}
	}
	return nil
}
