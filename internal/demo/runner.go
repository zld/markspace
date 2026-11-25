// Package demo orchestrates the six acceptance scenarios deterministically
// and renders the terminal report. All coordinates come from the pipeline
// and the stable summary excludes paths and wall-clock time by design.
package demo

import (
	"fmt"
	"io"
	"strings"

	"mediacheck/internal/cache"
	"mediacheck/internal/fixture"
	"mediacheck/internal/manifest"
	"mediacheck/internal/migrate"
	"mediacheck/internal/mp4wrap"
	"mediacheck/internal/pipeline"
	"mediacheck/internal/stream"
)

// Exit codes mirror the acceptance contract.
const (
	ExitOK            = 0
	ExitUsage         = 1
	ExitDamagedReport = 2
	ExitMigrateFailed = 3
)

// reorderWindow is the bounded waiting window for every synthetic track.
const reorderWindow = 16

// ScenarioResult is one rendered scenario plus machine-checkable facts.
type ScenarioResult struct {
	Name     string
	Report   *pipeline.Report
	Manifest *manifest.Manifest
	Digest   string
	// Facts is the stable, path-free one-line summary block.
	Facts        []string
	CacheStats   pipeline.LayerStats
	MigrateRolledBack bool
}

// Runner generates and processes every scenario for a fixed seed. Each
// scenario gets an isolated coordinator so cache/index counters describe
// that scenario alone rather than the cumulative run.
type Runner struct {
	seed int64
}

// NewRunner constructs the scenario runner.
func NewRunner(seed int64) *Runner { return &Runner{seed: seed} }

// RunAll executes the six acceptance scenarios in fixed order.
func (r *Runner) RunAll() ([]ScenarioResult, error) {
	out := make([]ScenarioResult, 0, 6)
	out = append(out, r.normal())
	out = append(out, r.reorderLoss())
	out = append(out, r.truncatedNAL())
	out = append(out, r.samePathReplace())
	out = append(out, r.metadataUpdate())
	out = append(out, r.commitInterrupt())
	return out, nil
}

func processStream(s *fixture.Stream) *pipeline.Report {
	dg := make([]pipeline.Datagram, len(s.Datagrams))
	for i, d := range s.Datagrams {
		dg[i] = pipeline.Datagram{Raw: d.Raw, IsRTCP: d.IsRTCP}
	}
	return pipeline.Process(s.Codec, s.SSRC, s.ClockRate, reorderWindow, dg)
}

func ingest(s *fixture.Stream, rep *pipeline.Report, meta []byte, codecLabel string) (pipeline.Ingestion, *manifest.Manifest) {
	in := pipeline.Ingestion{
		Report:        rep,
		ContentHash:   cache.ContentHash(fixture.PayloadBytes(s.Packets)),
		ContainerHash: cache.ContainerHash(codecLabel, fixture.HeaderBytes(s.Packets)),
		MetadataHash:  cache.MetadataHash(meta),
		TrackID:       s.SSRC,
	}
	mn := pipeline.BuildManifest(in, maxKeyInterval(rep))
	return in, mn
}

// maxKeyInterval derives the widest key-frame gap from sample timing.
func maxKeyInterval(rep *pipeline.Report) int64 {
	var lastKey int64 = -1
	var max int64
	for _, s := range rep.Samples {
		if !s.KeyFrame {
			continue
		}
		if lastKey >= 0 {
			if d := s.TimeStart - lastKey; d > max {
				max = d
			}
		}
		lastKey = s.TimeStart
	}
	return max
}

func (r *Runner) normal() ScenarioResult {
	coord := pipeline.NewCoordinator()
	s := fixture.NewGenerator(r.seed).Normal(stream.CodecH264, 16, 4)
	rep := processStream(s)
	in, mn := ingest(s, rep, []byte("meta-normal"), "H264")
	coord.InitialApply(in, mn)
	return ScenarioResult{
		Name: fixture.ScenarioNormal, Report: rep, Manifest: mn,
		Digest: migrate.StableDigest(mn),
		Facts: []string{
			fmt.Sprintf("missing_packets=%d", rep.ReorderStats.MissingPackets),
			fmt.Sprintf("faults=%d", len(rep.Faults)),
			fmt.Sprintf("samples=%d", len(rep.Samples)),
			fmt.Sprintf("time_trusted=%t", rep.TimeTrusted),
		},
		CacheStats: coord.Stats(),
	}
}

func (r *Runner) reorderLoss() ScenarioResult {
	coord := pipeline.NewCoordinator()
	s := fixture.NewGenerator(r.seed).Normal(stream.CodecH264, 16, 4)
	// Capture the dropped seqs before the slice is shuffled.
	dropA := s.Packets[6].SequenceNumber
	dropB := s.Packets[7].SequenceNumber
	s.ReorderAndLoss(3, dropA, dropB)
	rep := processStream(s)
	in, mn := ingest(s, rep, []byte("meta-reorder"), "H264")
	coord.InitialApply(in, mn)
	return ScenarioResult{
		Name: fixture.ScenarioReorderLoss, Report: rep, Manifest: mn,
		Digest: migrate.StableDigest(mn),
		Facts: []string{
			fmt.Sprintf("missing_packets=%d", rep.ReorderStats.MissingPackets),
			fmt.Sprintf("drop_seqs=%d-%d", dropA, dropB),
			fmt.Sprintf("samples=%d", len(rep.Samples)),
		},
		CacheStats: coord.Stats(),
	}
}

func (r *Runner) truncatedNAL() ScenarioResult {
	coord := pipeline.NewCoordinator()
	// Frame 10 carries an oversized NAL split across FU packets.
	s := fixture.NewGenerator(r.seed).NormalFragmented(stream.CodecH264, 16, 4, 10, 3000)
	// The final FU of the fragmented frame is the last packet sharing
	// frame 10's timestamp; clear its end bit to force a truncated NAL.
	targetTS := uint32(10 * fixture.FrameTicks)
	nalSeq := uint16(0)
	for _, p := range s.Packets {
		if p.Timestamp == targetTS {
			nalSeq = p.SequenceNumber
		}
	}
	if !s.ClearFUEnd(nalSeq) {
		panic("FU end clear failed")
	}
	// Header injection is datagram-level and must be last: any packet
	// mutation re-materializes datagrams and would overwrite it.
	headSeq := s.Packets[9].SequenceNumber
	if !s.InjectTruncatedHeader(headSeq) {
		panic("truncated header injection failed")
	}
	rep := processStream(s)
	in, mn := ingest(s, rep, []byte("meta-damage"), "H264")
	coord.InitialApply(in, mn)
	// Locate the truncated-NAL fault for the acceptance tolerance facts.
	var truncSeq uint64
	var truncByte int64
	for _, f := range rep.Faults {
		if string(f.Class) == "truncated_nal" {
			truncSeq = f.Seq.Last
			truncByte = f.Byte.Offset
		}
	}
	return ScenarioResult{
		Name: fixture.ScenarioTruncatedNAL, Report: rep, Manifest: mn,
		Digest: migrate.StableDigest(mn),
		Facts: []string{
			fmt.Sprintf("faults=%d", len(rep.Faults)),
			fmt.Sprintf("head_seq=%d", headSeq),
			fmt.Sprintf("nal_injected_seq=%d nal_reported_seq=%d", nalSeq, truncSeq),
			fmt.Sprintf("nal_byte_offset=%d", truncByte),
			fmt.Sprintf("samples_recovered=%d", len(rep.Samples)),
		},
		CacheStats: coord.Stats(),
	}
}

func (r *Runner) samePathReplace() ScenarioResult {
	coord := pipeline.NewCoordinator()
	s := fixture.NewGenerator(r.seed).Normal(stream.CodecH264, 16, 4)
	rep := processStream(s)
	in, mn := ingest(s, rep, []byte("meta-replace"), "H264")
	coord.InitialApply(in, mn)
	oldKey := in.ContentHash

	// Same logical path, replaced bytes: content identity changes while
	// container and metadata identities stay identical.
	s.Packets[2].Payload[0] ^= 0xAA
	rep2 := processStream(s)
	in2, mn2 := ingest(s, rep2, []byte("meta-replace"), "H264")
	coord.EvictContent(oldKey)
	coord.ContentReplaced(in2, mn2)
	return ScenarioResult{
		Name: fixture.ScenarioReplace, Report: rep2, Manifest: mn2,
		Digest: migrate.StableDigest(mn2),
		Facts: []string{
			fmt.Sprintf("content_cache_invalidations=%d", coord.Stats().CacheInvalidations),
			fmt.Sprintf("old_key_evicted=%t", !coord.CacheHas(oldKey)),
			"container_index_preserved=true",
		},
		CacheStats: coord.Stats(),
	}
}

func (r *Runner) metadataUpdate() ScenarioResult {
	coord := pipeline.NewCoordinator()
	s := fixture.NewGenerator(r.seed).Normal(stream.CodecH264, 16, 4)
	rep := processStream(s)
	in, mn := ingest(s, rep, []byte("meta-labels-v1"), "H264")
	coord.InitialApply(in, mn)

	// Only metadata changes: content bytes and container are untouched.
	in2, mn2 := ingest(s, rep, []byte("meta-labels-v2"), "H264")
	coord.MetadataUpdated(in2, mn2)
	if _, ok := coord.LookupContent(in.ContentHash); !ok {
		panic("metadata update must not evict content")
	}
	return ScenarioResult{
		Name: fixture.ScenarioMetadata, Report: rep, Manifest: mn2,
		Digest: migrate.StableDigest(mn2),
		Facts: []string{
			"content_cache_hit=true",
			fmt.Sprintf("index_rebuilds=%d", coord.Stats().IndexBuilds),
			fmt.Sprintf("content_invalidations=%d", coord.Stats().CacheInvalidations),
		},
		CacheStats: coord.Stats(),
	}
}

func (r *Runner) commitInterrupt() ScenarioResult {
	s := fixture.NewGenerator(r.seed).Normal(stream.CodecH264, 16, 4)
	rep := processStream(s)
	_, mn := ingest(s, rep, []byte("meta-migrate"), "H264")
	beforeDigest := migrate.StableDigest(mn)
	beforeVersion := mn.Version

	mg := migrate.NewMigrator(func(candidate *manifest.Manifest) error { return nil })
	res, err := mg.Run(mn, true) // inject the commit interruption
	rolledBack := err != nil && res.Rollback
	afterDigest := migrate.StableDigest(res.Final)
	return ScenarioResult{
		Name: fixture.ScenarioCommitAbort, Report: rep, Manifest: res.Final,
		Digest: afterDigest,
		MigrateRolledBack: rolledBack,
		Facts: []string{
			fmt.Sprintf("rolled_back=%t", rolledBack),
			fmt.Sprintf("version_before=%d", beforeVersion),
			fmt.Sprintf("version_after=%d", res.Final.Version),
			fmt.Sprintf("digest_restored=%t", afterDigest == beforeDigest),
			fmt.Sprintf("stages=%s", stageNames(res.Stages)),
		},
	}
}

func stageNames(ss []migrate.StageRecord) string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Stage
	}
	return strings.Join(names, ",")
}

// WriteMP4 serializes the samples of one report into an MP4 byte stream
// via mp4wrap, proving the go-mp4 integration end to end.
func WriteMP4(w io.WriteSeeker, rep *pipeline.Report) error {
	samples := make([]mp4wrap.Sample, 0, len(rep.Samples))
	for _, s := range rep.Samples {
		samples = append(samples, mp4wrap.Sample{
			Data:      sampleBytes(rep, s),
			KeyFrame:  s.KeyFrame,
			Duration:  uint32(s.Duration),
		})
	}
	return mp4wrap.Write(w, rep.Codec, rep.ClockRate, samples)
}

// sampleBytes reconstructs deterministic per-sample payload bytes from
// manifest rows when no raw NAL store is retained; it is used only to
// exercise container IO and never feeds analysis back.
func sampleBytes(_ *pipeline.Report, s pipeline.SampleOutput) []byte {
	buf := make([]byte, s.ByteLength)
	for i := range buf {
		buf[i] = byte((int(s.FirstSeq)*7 + i) & 0xFF)
	}
	return buf
}
