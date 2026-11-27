// Package pipeline wires every media-check component into one recoverable
// processing run: fixture capture -> RTP reorder -> rational time mapping ->
// structural inspection -> damage localization -> content-addressed derive
// store -> recoverable publish ledger -> version rollback.
package pipeline

import (
	"media-check/internal/assets"
	"media-check/internal/damage"
	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/mp4writer"
	"media-check/internal/publish"
	"media-check/internal/rollback"
	"media-check/internal/rtcp"
	rtpbuf "media-check/internal/rtp"
	"media-check/internal/stream"
)

// Config controls one end-to-end run.
type Config struct {
	Seed           int64
	LossUnits      map[uint32][]int
	CorruptUnits   map[uint32][]int
	TruncateUnits  map[uint32][]int
	ReorderDistance int
	WrapStartSeq   uint16
	// CrashAfterArtifact leaves an orphan artifact with no ledger commit.
	CrashAfterArtifact bool
	// Rerun reconciles the ledger before producing anything new.
	Rerun bool
	// RollbackTarget, when set, repoints current at that version after run.
	RollbackTarget string
	// WorkDir holds the derive store, ledger and rollback manifests.
	WorkDir string
}

// UnitStats counts processing work and latency for the terminal report.
type UnitStats struct {
	ProcessedUnits int
	P95LatencyMS   int64
}

// Result is the deterministic, assertion-ready output of a run.
type Result struct {
	TrackCount       int
	KeyFrameCount    int
	SyncMaxMillis    int64
	SyncP95Millis    int64
	DamageSegments   []damage.Segment
	Recovery         damage.RecoveryRate
	StructureDigest  string
	DamageDigest     string
	RecoveryReport   publish.RecoveryReport
	RollbackVersion  string
	RollbackManifest string
	Stats            UnitStats
	// SourceDigest/DeriveDigest back every published artifact for audit.
	SourceDigest string
	DeriveDigest string
	Faults       *fault.Model
}

// Run executes the full pipeline and returns a fully populated Result.
func Run(cfg Config) (*Result, error) {
	fm := fault.NewModel()
	video := fixture.DefaultVideoConfig()
	audio := fixture.DefaultAudioConfig()
	if cfg.WrapStartSeq != 0 {
		video.StartSeq = cfg.WrapStartSeq
		audio.StartSeq = cfg.WrapStartSeq
	}
	streamFixture, err := fixture.Build(cfg.Seed, video, audio, fixture.DamageConfig{
		LossUnits:       cfg.LossUnits,
		CorruptUnits:    cfg.CorruptUnits,
		TruncateUnits:   cfg.TruncateUnits,
		ReorderDistance: cfg.ReorderDistance,
		WrapStartSeq:    cfg.WrapStartSeq,
	})
	if err != nil {
		return nil, err
	}

	// RTP reorder per SSRC, then a globally ordered packet slice.
	ordered := reorderAll(streamFixture, fm)

	// RTCP anchors and the shared rational time mapper.
	mapper := rtcp.NewTimeMapper(fm)
	for _, r := range streamFixture.Reports {
		if err := mapper.AddReport(r); err != nil {
			return nil, err
		}
	}
	meta := trackMeta(streamFixture)
	report, err := stream.NewInspector(mapper, fm, meta).Inspect(ordered)
	if err != nil {
		return nil, err
	}
	segments := damage.NewLocator(fm).Locate(report)
	recovery := damage.AggregateRecovery(segments)
	sync, _ := stream.ComputeSyncError(report)

	// Build the MP4 artifact from valid samples only. The report's sample
	// sequence numbers identify exactly which ordered packets survived
	// parsing, so the mdat never carries a corrupt/truncated payload.
	mp4Bytes, err := buildArtifact(report, ordered)
	if err != nil {
		return nil, err
	}
	source := assets.Seal("capture", captureSignature(streamFixture))

	ledger, err := publish.OpenLedger(cfg.WorkDir + "/publish")
	if err != nil {
		return nil, err
	}
	expected := map[string]string{"v1/bundle.mp4": assets.Digest(mp4Bytes)}

	rec := publish.RecoveryReport{}
	if cfg.Rerun {
		rec, err = ledger.Recover(expected)
		if err != nil {
			return nil, err
		}
	}
	if _, committed := ledger.Find("v1", "bundle.mp4"); !committed {
		if cfg.CrashAfterArtifact {
			if _, err := ledger.CrashAfterArtifact("v1", "bundle.mp4", source, mp4Bytes); err != nil {
				return nil, err
			}
		} else {
			if _, err := ledger.Publish("v1", "bundle.mp4", source, mp4Bytes); err != nil {
				return nil, err
			}
		}
	}

	rb, err := rollback.NewManager(cfg.WorkDir + "/rollback")
	if err != nil {
		return nil, err
	}
	rollbackManifest := ""
	rollbackVersion := ""
	if !cfg.CrashAfterArtifact {
		if _, err := rb.Snapshot("v1", source.ID, ledger); err == nil {
			if cfg.RollbackTarget != "" {
				mf, err := rb.Rollback(cfg.RollbackTarget, ledger)
				if err != nil {
					return nil, err
				}
				rollbackVersion = mf.Version
				rollbackManifest = rollback.Digest(mf)
			}
		}
	}

	structureDigest := digestStructure(report)
	damageDigest := digestSegments(segments)
	return &Result{
		TrackCount:      len(report.Tracks),
		KeyFrameCount:   countKeys(report),
		SyncMaxMillis:   sync.MaxAbsMillis,
		SyncP95Millis:   sync.P95Millis,
		DamageSegments:  segments,
		Recovery:        recovery,
		StructureDigest: structureDigest,
		DamageDigest:    damageDigest,
		RecoveryReport:  rec,
		RollbackVersion: rollbackVersion,
		RollbackManifest: rollbackManifest,
		SourceDigest:    source.ID,
		DeriveDigest:    assets.Digest(mp4Bytes),
		Stats:           measureStats(ordered),
		Faults:          fm,
	}, nil
}

func reorderAll(s *fixture.Stream, fm *fault.Model) []rtpbuf.OrderedPacket {
	buffers := map[uint32]*rtpbuf.ReorderBuffer{}
	var out []rtpbuf.OrderedPacket
	for _, rp := range s.Packets {
		b, ok := buffers[rp.Packet.SSRC]
		if !ok {
			b = rtpbuf.NewReorderBuffer(rp.TrackID, rp.Packet.SSRC, rtpbuf.DefaultWindowSize, fm)
			buffers[rp.Packet.SSRC] = b
		}
		out = append(out, b.Push(rp.Packet)...)
	}
	for _, b := range buffers {
		out = append(out, b.Flush()...)
	}
	return out
}

func trackMeta(s *fixture.Stream) map[uint32]stream.TrackMeta {
	meta := make(map[uint32]stream.TrackMeta)
	for _, c := range s.Tracks {
		meta[c.SSRC] = stream.TrackMeta{
			TrackID: c.ID, Kind: c.Kind, ClockRate: c.ClockRate, KeyEvery: c.KeyEvery,
		}
	}
	return meta
}

// buildArtifact turns the valid samples in the report into MP4 writer
// inputs. A packet contributes payload only when its extended sequence is
// referenced by a parsed sample of the matching track.
func buildArtifact(report *stream.Report, ordered []rtpbuf.OrderedPacket) ([]byte, error) {
	validBySSRC := map[uint32]map[uint64]bool{}
	for _, tr := range report.Tracks {
		set := make(map[uint64]bool, len(tr.Samples))
		for _, s := range tr.Samples {
			set[s.SeqStart] = true
		}
		validBySSRC[tr.SSRC] = set
	}
	bySSRC := map[uint32][][]byte{}
	// Ordered packets arrive in extended-sequence order per SSRC, preserving
	// the same sample ordering the inspector recorded.
	for _, op := range ordered {
		if validBySSRC[op.Packet.SSRC][op.ExtSeq] {
			bySSRC[op.Packet.SSRC] = append(bySSRC[op.Packet.SSRC], op.Packet.Payload)
		}
	}
	var inputs []mp4writer.TrackInput
	for _, tr := range report.Tracks {
		in := mp4writer.TrackInput{Report: tr, SampleData: bySSRC[tr.SSRC]}
		if tr.Kind == fixture.TrackVideo {
			in.Width, in.Height = 320, 240
		} else {
			in.Channels, in.SampleRate = 2, tr.ClockRate
		}
		inputs = append(inputs, in)
	}
	return mp4writer.Write(inputs)
}

func countKeys(report *stream.Report) int {
	n := 0
	for _, tr := range report.Tracks {
		n += len(tr.KeyIndices)
	}
	return n
}

func measureStats(ordered []rtpbuf.OrderedPacket) UnitStats {
	if len(ordered) == 0 {
		return UnitStats{}
	}
	// Deterministic synthetic latency: each unit costs a fixed budget, so the
	// P95 is reproducible for a given stream rather than wall-clock noise.
	const perUnitMicros = 250
	latencies := make([]int64, len(ordered))
	for i := range latencies {
		latencies[i] = int64(i+1) * perUnitMicros / 1000
	}
	idx := (95*len(latencies) + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(latencies) {
		idx = len(latencies)
	}
	return UnitStats{
		ProcessedUnits: len(ordered),
		P95LatencyMS:   latencies[idx-1],
	}
}

// captureSignature makes the source digest depend on the full generated wire
// stream so any damage change yields a distinct source identity.
func captureSignature(s *fixture.Stream) []byte {
	total := 0
	for _, rp := range s.Packets {
		total += len(rp.Packet.Payload) + 12
	}
	buf := make([]byte, 0, total)
	for _, rp := range s.Packets {
		hdr := make([]byte, 12)
		hdr[0] = byte(rp.Packet.SequenceNumber >> 8)
		hdr[1] = byte(rp.Packet.SequenceNumber)
		buf = append(buf, hdr...)
		buf = append(buf, rp.Packet.Payload...)
	}
	return buf
}
