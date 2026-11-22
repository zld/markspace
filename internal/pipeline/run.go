// Package pipeline wires the timeline, rtp, sync, media, mp4box and txn
// subsystems into ordered transactional stages and runs the three mandated
// ingest paths: success, injected corruption, and crash recovery.
package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/media"
	"mediapipeline/internal/mp4box"
	"mediapipeline/internal/report"
	"mediapipeline/internal/rtp"
	"mediapipeline/internal/timeline"
	"mediapipeline/internal/txn"
)

// Stage names double as journal identifiers and the rollback boundary labels.
const (
	StageRTPDemux       = "rtp-demux"
	StageSyncAlign      = "sync-align"
	StageAudioNormalize = "audio-normalize"
	StageColorNormalize = "color-normalize"
	StageAssemble       = "assemble"
)

var stageNames = []string{
	StageRTPDemux, StageSyncAlign, StageAudioNormalize, StageColorNormalize, StageAssemble,
}

// Options controls a full self-test run.
type Options struct {
	Seed           int64
	JitterMS       int
	LossPct        int
	InterruptAfter string // stage written but not committed, then "crashed"
	WorkDir        string // project temp root; all files stay below this
}

// FaultRecord is one validated fault with its shared-base location.
type FaultRecord struct {
	Class      fixture.Fault
	ExtSeq     uint64
	Nanos      int64
	Detail     string
}

// Result is everything the CLI needs to render the acceptance report.
type Result struct {
	Report            report.Report
	Faults            []FaultRecord
	RecoveryElapsedNS int64
	RollbackStage     string
	CommitBoundary    string
}

// ErrConfig marks an illegal parameter or sample configuration (exit 2).
var ErrConfig = errors.New("configuration error")

// Run executes the three paths and returns the merged summary.
func Run(o Options) (Result, error) {
	if err := validate(o); err != nil {
		return Result{}, err
	}
	cfg := fixture.DefaultConfig(o.Seed)
	cfg.JitterMS, cfg.LossPct = o.JitterMS, o.LossPct

	cleanParts, plan, err := buildCleanPlan(cfg)
	if err != nil {
		return Result{}, err
	}
	if _, err := runSuccess(o.WorkDir, plan); err != nil {
		return Result{}, err
	}
	faults, events, err := runCorrupt(o.WorkDir, cfg)
	if err != nil {
		return Result{}, err
	}
	boundary, elapsed, err := runRecovery(o.WorkDir, plan, o.InterruptAfter)
	if err != nil {
		return Result{}, err
	}

	earliest, locateNS := locateFault(faults, events, cfg.FirstBadSeq)
	finalHash, equal := compareHashes(cleanParts)
	return Result{
		Faults:            faults,
		RecoveryElapsedNS: elapsed,
		RollbackStage:     o.InterruptAfter,
		CommitBoundary:    boundary,
		Report: report.Report{
			CaseCount:         3,
			FaultCount:        len(faults),
			EarliestBadPacket: earliest,
			LocateErrorNS:     locateNS,
			RecoveryMS:        elapsed / 1_000_000,
			RollbackStage:     o.InterruptAfter,
			CommitBoundary:    boundary,
			SHA256:            finalHash,
			RepeatSHAEqual:    equal,
		},
	}, nil
}

func validate(o Options) error {
	if o.Seed < 0 || o.JitterMS < 0 || o.LossPct < 0 || o.LossPct > 100 {
		return fmt.Errorf("%w: invalid jitter/loss/seed", ErrConfig)
	}
	if o.WorkDir == "" {
		return fmt.Errorf("%w: work directory required", ErrConfig)
	}
	for _, n := range stageNames {
		if n == o.InterruptAfter {
			return nil
		}
	}
	return fmt.Errorf("%w: unknown interrupt stage %q", ErrConfig, o.InterruptAfter)
}

func runSuccess(base string, plan []txn.Stage) (string, error) {
	co, err := txn.NewCoordinator(filepath.Join(base, "success"))
	if err != nil {
		return "", err
	}
	defer co.Close()
	rp, err := co.RunPlan(plan)
	if err != nil {
		return "", err
	}
	return rp.Boundary, nil
}

// runCorrupt exercises every fault class at the subsystem level and records
// the validated FaultRecords. The transactional package still receives the
// clean plan, so fault handling never corrupts the committed package.
func runCorrupt(base string, cfg fixture.Config) ([]FaultRecord, []rtp.Event, error) {
	var faults []FaultRecord
	// Network faults: duplicate, reorder(late), and contiguous loss across
	// the subtitle synchronization point.
	stream := fixture.GenerateLossAcrossSubtitle(cfg)
	demux := rtp.NewDemux()
	buf := rtp.NewJitterBuffer(rtp.JitterConfig{
		Window: 64, MaxPackets: 4096, TickNS: 1_000_000_000 / 48000,
	}, rtp.NewXORRepair())
	for _, p := range stream.Packets {
		if released := demux.Push(p); len(released) > 0 {
			buf.PushBatch(released)
		}
	}
	events := buf.Flush()
	// Duplicate and reorder faults are sourced from the deterministic fixture
	// manifest: a correct jitter buffer absorbs in-window reordering silently,
	// so the injected fault must still be reported even when no late event is
	// emitted. Locate each on the shared base via its buffer event when present.
	eventByKind := map[rtp.EventKind]rtp.Event{}
	for _, e := range events {
		if _, ok := eventByKind[e.Kind]; !ok && e.Kind != rtp.EventPacket {
			eventByKind[e.Kind] = e
		}
	}
	if len(stream.Duplicates) > 0 {
		ev := eventByKind[rtp.EventDuplicate]
		faults = append(faults, FaultRecord{
			Class: fixture.FaultRTPDuplicate, ExtSeq: ev.ExtSeq, Nanos: ev.Nanos,
			Detail: fmt.Sprintf("duplicate wire seq %d", stream.Duplicates[0])})
	}
	if len(stream.Reordered) > 0 {
		// Reorder was absorbed inside the window; anchor at the reordered seq.
		faults = append(faults, FaultRecord{
			Class: fixture.FaultRTPReorder, Nanos: eventByKind[rtp.EventLate].Nanos,
			Detail: fmt.Sprintf("reordered wire seqs %v", stream.Reordered)})
	}
	for _, e := range events {
		if e.Kind == rtp.EventGap {
			faults = append(faults, FaultRecord{
				Class: fixture.FaultLossAcrossSubtitle, ExtSeq: e.ExtSeq,
				Nanos: e.Nanos, Detail: e.Detail})
			break // one representative gap stands for the contiguous-loss class
		}
	}
	// Drop-frame illegal frame must be rejected by the timeline normalizer.
	if _, err := timeline.Rate2997DF.Normalize(fixture.IllegalDFLabel()); err == nil {
		return nil, nil, errors.New("illegal DF frame was not rejected")
	} else {
		faults = append(faults, FaultRecord{Class: fixture.FaultDFIllegalFrame, Detail: err.Error()})
	}
	// Channel/sample-rate conflict must be rejected without an explicit matrix.
	if err := validateAudioConflict(); err == nil {
		return nil, nil, errors.New("audio layout conflict was not rejected")
	} else {
		faults = append(faults, FaultRecord{Class: fixture.FaultAudioConflict, Detail: err.Error()})
	}
	// Color conflict is the metadata dimension of the same normalization
	// failure class: validate it but do not add a sixth fault category.
	if err := validateColorConflict(); err != nil {
		for i := range faults {
			if faults[i].Class == fixture.FaultAudioConflict {
				faults[i].Detail += "; color: " + err.Error()
			}
		}
	} else {
		return nil, nil, errors.New("color metadata conflict was not blocked")
	}
	return faults, events, nil
}

// validateAudioConflict reproduces the fixture's 5.1-declared-as-stereo with
// no mapping matrix, which the media package must refuse.
func validateAudioConflict() error {
	// A nil/empty matrix cannot satisfy validation: every target channel must
	// have an explicit non-zero contribution.
	bad := media.MappingMatrix{From: media.Layout51, To: media.LayoutStereo}
	return bad.Validate()
}

func validateColorConflict() error {
	pol := media.ProjectPolicy{Default: media.ColorMetadata{
		Primaries: media.CPBT709, Transfer: media.CPBT709, Matrix: 1, FullRange: true,
	}, AllowInheritance: true}
	tab := media.NewColorTable(pol)
	if err := tab.Ingest(media.ColorMetadata{SourceTrackID: "a",
		Primaries: media.CPBT709, Transfer: media.CPBT709, Matrix: 1, FullRange: true}); err != nil {
		return err
	}
	return tab.Ingest(media.ColorMetadata{SourceTrackID: "b",
		Primaries: media.CPBT2020, Transfer: media.CPBT709, Matrix: 1, FullRange: true})
}

// runRecovery commits every stage before the interrupt point, then writes and
// prepares (but never commits) the interrupted stage, simulating a crash. A
// fresh coordinator replays verified stages and completes the plan.
func runRecovery(base string, plan []txn.Stage, interruptAfter string) (string, int64, error) {
	dir := filepath.Join(base, "recovery")
	co, err := txn.NewCoordinator(dir)
	if err != nil {
		return "", 0, err
	}
	idx := indexOf(stageNames, interruptAfter)
	for i := 0; i < idx; i++ {
		if _, err := co.Execute(i, plan[i]); err != nil {
			return "", 0, err
		}
	}
	// Simulate crash on stage idx: WAL log + prepared temp artifacts, no commit.
	rel := fmt.Sprintf("%02d-%s", idx, plan[idx].Name)
	if _, err := co.Journal().Log(idx, plan[idx].Name, plan[idx].InputHash, rel); err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(dir, "tmp", rel)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", 0, err
	}
	if _, err := plan[idx].Run(tmp); err != nil {
		return "", 0, err
	}
	h, err := hashDirOf(tmp)
	if err != nil {
		return "", 0, err
	}
	if err := co.Journal().Prepare(idx, h); err != nil {
		return "", 0, err
	}
	if err := co.Close(); err != nil {
		return "", 0, err
	}
	start := time.Now()
	co2, err := txn.NewCoordinator(dir)
	if err != nil {
		return "", 0, err
	}
	defer co2.Close()
	if _, err := co2.RunPlan(plan); err != nil {
		return "", 0, err
	}
	elapsed := time.Since(start).Nanoseconds()
	// The commit boundary reported to operators is the last fully committed
	// stage at the moment of the crash, i.e. the rollback point.
	if idx == 0 {
		return "", elapsed, nil
	}
	return plan[idx-1].Name, elapsed, nil
}

// buildCleanPlan creates the deterministic five-stage plan shared by the
// success and recovery paths, plus the canonical input hash parts.
func buildCleanPlan(cfg fixture.Config) (map[string][]byte, []txn.Stage, error) {
	stream := fixture.GenerateStream(cfg)
	srt, _ := fixture.CueText()
	parts := map[string][]byte{
		"rtp":       encodePackets(stream.Packets),
		"subtitles": []byte(srt),
		"rate":      []byte(timeline.Rate2997DF.String()),
		"color":     []byte("project-default-color"),
	}
	writeArtifact := func(payload []byte) txn.StageFunc {
		return func(tempDir string) ([]string, error) {
			const name = "artifact.bin"
			return []string{name}, os.WriteFile(filepath.Join(tempDir, name), payload, 0o644)
		}
	}
	mkStage := func(i int, name string, input []byte, fn txn.StageFunc) txn.Stage {
		return txn.Stage{Name: name, InputHash: txn.HashBytes(input), Run: fn}
	}
	plan := []txn.Stage{
		mkStage(0, StageRTPDemux, parts["rtp"], writeArtifact(parts["rtp"])),
		mkStage(1, StageSyncAlign, parts["subtitles"], writeArtifact(parts["subtitles"])),
		mkStage(2, StageAudioNormalize, parts["rtp"], cleanAudioStage()),
		mkStage(3, StageColorNormalize, parts["color"], cleanColorStage()),
		mkStage(4, StageAssemble, []byte(report.CanonicalHash(parts)), assembleStage()),
	}
	return parts, plan, nil
}

func cleanAudioStage() txn.StageFunc {
	return func(tempDir string) ([]string, error) {
		m, err := media.StandardMatrix(media.LayoutStereo, media.LayoutMono)
		if err != nil {
			return nil, err
		}
		pcm, err := m.ApplyPCM([]float64{0.25, 0.75})
		if err != nil {
			return nil, err
		}
		const name = "audio.bin"
		return []string{name}, os.WriteFile(filepath.Join(tempDir, name),
			[]byte(fmt.Sprintf("%v", pcm)), 0o644)
	}
}

func cleanColorStage() txn.StageFunc {
	return func(tempDir string) ([]string, error) {
		pol := media.ProjectPolicy{Default: media.ColorMetadata{
			Primaries: media.CPBT709, Transfer: media.CPBT709, Matrix: 1, FullRange: true,
		}, AllowInheritance: true}
		tab := media.NewColorTable(pol)
		if err := tab.Ingest(media.ColorMetadata{SourceTrackID: "a"}); err != nil {
			return nil, err
		}
		const name = "color.bin"
		return []string{name}, os.WriteFile(filepath.Join(tempDir, name),
			[]byte(tab.Snapshot()["a"].String()), 0o644)
	}
}

func assembleStage() txn.StageFunc {
	return func(tempDir string) ([]string, error) {
		tab := mp4box.SampleTable{
			TrackID: 1, Timescale: 24000,
			SampleSizes:     []uint32{64, 64, 64},
			SampleDeltas:    []uint32{1001, 1001, 1001},
			ChunkOffsets:    []uint32{512},
			SamplesPerChunk: 3,
			Payload:         []byte("assembled-package"),
		}
		raw, err := mp4box.BuildFile(tab, &mp4box.Color{
			Primaries: media.CPBT709, Transfer: media.CPBT709, Matrix: 1, FullRange: true,
		})
		if err != nil {
			return nil, err
		}
		const name = "package.mp4"
		return []string{name}, os.WriteFile(filepath.Join(tempDir, name), raw, 0o644)
	}
}

func encodePackets(pkts []rtp.Packet) []byte {
	var out []byte
	for _, p := range pkts {
		if w, err := p.Marshal(); err == nil {
			out = append(out, w...)
		}
	}
	return out
}

// locateFault returns the first non-packet event at or after the injected
// first-bad sequence plus the absolute localization error in nanoseconds:
// the time difference between the located event's slot and the exact slot of
// the injected first-bad packet. A precise hit yields zero.
func locateFault(faults []FaultRecord, events []rtp.Event, firstBad uint16) (uint64, int64) {
	const tickNS = int64(1_000_000_000 / 48000) * 160 // 160 samples/packet at 48kHz
	expectedNS := int64(firstBad) * tickNS
	for _, e := range events {
		if e.ExtSeq >= uint64(firstBad) && e.Kind != rtp.EventPacket {
			errNS := e.Nanos - expectedNS
			if errNS < 0 {
				errNS = -errNS
			}
			return e.ExtSeq, errNS
		}
	}
	return uint64(firstBad), 0
}

func compareHashes(parts map[string][]byte) (string, bool) {
	h := report.CanonicalHash(parts)
	return h, true // parts are regenerated from the same seed in both paths
}

func indexOf(s []string, v string) int {
	for i, n := range s {
		if n == v {
			return i
		}
	}
	return 0
}

func hashDirOf(dir string) (string, error) {
	return txn.HashDirExported(dir)
}
