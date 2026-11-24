package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediarc/internal/buffer"
	"mediarc/internal/checkpoint"
	"mediarc/internal/ingest"
	"mediarc/internal/model"
	"mediarc/internal/report"
)

// Paths groups local workdir locations.
type Paths struct {
	Root        string
	InputDir    string
	Frames      string
	Audio       string
	Fixture     string
	FinalMP4    string
	SummaryJSON string
}

// NewPaths returns the stable workdir layout.
func NewPaths(workdir string) Paths {
	in := filepath.Join(workdir, "input")
	return Paths{
		Root: workdir, InputDir: in,
		Frames:      filepath.Join(in, "frames.mrc1"),
		Audio:       filepath.Join(in, "audio.mrtp"),
		Fixture:     filepath.Join(in, "fixture.json"),
		FinalMP4:    filepath.Join(workdir, "recovered.mp4"),
		SummaryJSON: filepath.Join(workdir, "summary.json"),
	}
}

// FixtureManifest persists generated injection locations for exact reporting.
type FixtureManifest struct {
	Frames *ingest.FrameFixture `json:"frames"`
	Audio  *ingest.RTPFixture   `json:"audio"`
}

// DemoResult contains generation facts and the printable summary.
type DemoResult struct {
	Summary      report.Summary
	FrameFixture *ingest.FrameFixture
	RTPFixture   *ingest.RTPFixture
	Faults       *model.Collector
}

// GenerateDemo creates deterministic sample inputs inside Workdir.
func GenerateDemo(o Options) (*DemoResult, error) {
	if o.Frames == 0 {
		o.Frames = 120
	}
	if o.AudioPackets == 0 {
		o.AudioPackets = 480
	}
	if o.ChunkSize == 0 {
		o.ChunkSize = 16
	}
	if _, _, err := o.Layouts(); err != nil {
		return nil, err
	}
	p := NewPaths(o.Workdir)
	if err := os.MkdirAll(p.InputDir, 0o755); err != nil {
		return nil, err
	}
	// `demo` creates a brand-new fixture. Clear stale completion state so the
	// documented demo -> interrupt -> resume sequence is repeatable in the same
	// --workdir. `resume` itself never performs this reset.
	_ = os.RemoveAll(filepath.Join(o.Workdir, "chunks"))
	_ = os.Remove(filepath.Join(o.Workdir, "index.json"))
	_ = os.Remove(p.FinalMP4)
	_ = os.Remove(p.SummaryJSON)
	badSHA := o.HasInject("bad-sha") || o.HasInject("bad_sha")
	truncated := o.HasInject("truncated") || o.HasInject("truncate")
	late := o.HasInject("late")
	duplicate := o.HasInject("duplicate")

	ffx, err := ingest.GenerateFrameFile(ingest.FrameGenOptions{
		Path: p.Frames, Seed: o.Seed, Frames: o.Frames,
		BadSHA: badSHA, Truncated: truncated, Duplicate: duplicate, OutOfOrder: true,
	})
	if err != nil {
		return nil, err
	}
	afx, err := ingest.GenerateRTPFile(ingest.RTPGenOptions{
		Path: p.Audio, Seed: o.Seed, Packets: o.AudioPackets,
		Channels: 2, SampleRate: orDefault32(o.TargetRate, model.AudioClockRate),
		Late: late, Duplicate: duplicate, OutOfOrder: true,
	})
	if err != nil {
		return nil, err
	}
	manifest := FixtureManifest{Frames: ffx, Audio: afx}
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.Fixture, mb, 0o644); err != nil {
		return nil, err
	}
	cfgBytes, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(ConfigPath(o.Workdir), cfgBytes, 0o644); err != nil {
		return nil, err
	}

	faults := &model.Collector{}
	metas, err := ingest.ScanFrameFile(p.Frames, faults)
	if err != nil {
		return nil, err
	}
	packets, err := ingest.ReadRTPFile(p.Audio, faults)
	if err != nil {
		return nil, err
	}
	if o.Window == 0 {
		o.Window = 64
	}
	if o.LateThreshold == 0 {
		o.LateThreshold = 8
	}
	jb := buffer.New(o.Window, o.LateThreshold, model.AudioClockRate, faults)
	for _, pkt := range packets {
		jb.Push(pkt)
	}
	_, _ = jb.Flush()
	jstats := jb.Stats()
	inputSHA, err := CombinedInputHash(p)
	if err != nil {
		return nil, err
	}
	locErr := locationError(faults.All(), ffx)
	valid := 0
	for _, m := range metas {
		if m.Valid {
			valid++
		}
	}
	sum := report.Summary{
		Mode: "demo", Workdir: o.Workdir, Seed: o.Seed,
		FrameTotal: o.Frames, RecordTotal: len(metas), ValidFrames: valid,
		BadSHA: faults.Count(model.FaultBadSHA), Truncated: faults.Count(model.FaultTruncated),
		AudioPackets: afx.CanonicalPackets, StoredAudioRecords: len(packets),
		LatePackets: jstats.Late, DuplicatePackets: jstats.Duplicate,
		OutOfOrderPackets: jstats.OutOfOrder, LostPackets: jstats.Lost,
		JitterEvents:     jstats.JitterEvents,
		LocationErrorMax: locErr, ChunkSize: o.ChunkSize, ChunkCount: ChunkCount(o.Frames, o.ChunkSize),
		InputSHA256: inputSHA, ExitCode: model.ExitOK,
	}
	sum.Locations = report.LocationsFromFaults(faults.All())
	return &DemoResult{Summary: sum, FrameFixture: ffx, RTPFixture: afx, Faults: faults}, nil
}

// ChunkCount returns ceil(frames/chunkSize).
func ChunkCount(frames, chunkSize int) int {
	if chunkSize <= 0 {
		return 0
	}
	return (frames + chunkSize - 1) / chunkSize
}

// CombinedInputHash hashes the two generated input files.
func CombinedInputHash(p Paths) (string, error) {
	h1, err := checkpoint.HashFile(p.Frames)
	if err != nil {
		return "", err
	}
	h2, err := checkpoint.HashFile(p.Audio)
	if err != nil {
		return "", err
	}
	return checkpoint.HashBytes([]byte(h1 + ":" + h2)), nil
}

func locationError(faults []model.Fault, fx *ingest.FrameFixture) int64 {
	var max int64
	abs := func(v int64) int64 {
		if v < 0 {
			return -v
		}
		return v
	}
	for _, f := range faults {
		var expected *ingest.InjectedLocation
		switch f.Kind {
		case model.FaultBadSHA:
			if fx != nil {
				expected = fx.BadSHA
			}
		case model.FaultTruncated:
			if fx != nil {
				expected = fx.Truncated
			}
		}
		if expected != nil {
			if d := abs(f.Offset - expected.Offset); d > max {
				max = d
			}
		}
	}
	return max
}
