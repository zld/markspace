package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

func segmentStates(scan *demux.FragmentScan) []SegmentState {
	states := make([]SegmentState, 0, len(scan.Segments))
	for _, seg := range scan.Segments {
		d := sha256.Sum256(seg.Payload)
		states = append(states, SegmentState{
			ID: seg.ID, MoofOffset: seg.MoofOffset, Size: seg.Size,
			Digest: hex.EncodeToString(d[:]), Committed: true,
		})
	}
	return states
}

func countDuplicates(states []SegmentState) int {
	seen := make(map[string]int, len(states))
	dups := 0
	for _, s := range states {
		key := s.Digest
		seen[key]++
		if seen[key] > 1 {
			dups++
		}
	}
	return dups
}

func containsAll(committed, recovered []SegmentState) bool {
	index := make(map[string]SegmentState, len(committed))
	for _, s := range committed {
		index[s.Digest] = s
	}
	for _, s := range recovered {
		if _, ok := index[s.Digest]; !ok {
			return false
		}
	}
	return true
}

func committedOutputMatches(path, wantDigest string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	got := OutputPayloadDigest(data)
	if wantDigest != "" && got != wantDigest {
		return "", false
	}
	return got, true
}

// RecoveryCheckpoint is the recovery breakpoint file.
type RecoveryCheckpoint struct {
	Input            string         `json:"input"`
	InputSHA256      string         `json:"input_sha256"`
	PayloadSHA256In  string         `json:"payload_sha256_in"`
	PayloadSHA256    string         `json:"payload_sha256"`
	EventsIn         int            `json:"events_in"`
	EventsOut        int            `json:"events_out"`
	EventOrderSHA256 string         `json:"event_order_sha256"`
	Tracks           []track.Entry  `json:"tracks"`
	Segments         []SegmentState `json:"segments"`
	UpdatedAt        string         `json:"updated_at"`
}

func writeRecoveryCheckpoint(dir string, cp *RecoveryCheckpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	buf, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "recovery.json.tmp")
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "recovery.json"))
}

func loadRecoveryCheckpoint(dir string) (*RecoveryCheckpoint, bool) {
	buf, err := os.ReadFile(filepath.Join(dir, "recovery.json"))
	if err != nil {
		return nil, false
	}
	var cp RecoveryCheckpoint
	if err := json.Unmarshal(buf, &cp); err != nil {
		return nil, false
	}
	return &cp, true
}

func failRecovery(sum RecoverySummary, err error) RecoverySummary {
	if fe, ok := fault.As(err); ok {
		sum.ErrorStage = string(fe.Stage)
		sum.Reason = fe.Reason
		if fe.HasOffset() {
			sum.InputOffset = fe.InputOffset
		}
	} else if err != nil {
		sum.Reason = err.Error()
	}
	return sum
}
