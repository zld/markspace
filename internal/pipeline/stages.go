package pipeline

import (
	"errors"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/dag"
	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/media"
	"mediasync/internal/mux"
	"mediasync/internal/rtp"
)

// MaxConcealPackets is the largest loss burst replaced by silence. Bursts
// longer than this cannot have their duration inferred and block the line.
const MaxConcealPackets int64 = 8

// Options parameterizes one pipeline run.
type Options struct {
	Seed          int64
	Duration      time.Duration
	LossBurst     int
	Reorder       int
	TruncateAudio int
	CorruptIndex  int
	RetryLimit    int
	JournalPath   string
	// FailNode/ForceFail drive the dependency-failure fixture: the named DAG
	// node returns an error on its first ForceFail attempts.
	FailNode  string
	ForceFail int
}

// parseOutput is the parse node's structured result.
type parseOutput struct {
	WAV          *media.WAVFile
	Truncated    bool
	TruncMissing int64
	InputDigest  string
	BlockAlign   int
	SampleRate   int
	FramesPerPkt int
}

// repairOutput is the clock-repair node result.
type repairOutput struct {
	Entries   []clock.TimeEntry
	Packets   []*rtp.Packet // packets carried through for recovery
	SyncError float64
	Counters  StageCounters
}

// recoverOutput is the recovery node result.
type recoverOutput struct {
	Intervals []rtp.Interval
	Gaps      []rtp.Gap
	Stats     rtp.Stats
	Packets   []*rtp.Packet
}

// indexOutput is the index node result, including MP4 serialization.
type indexOutput struct {
	Records       []index.Record
	Dropped       index.DropReport
	MP4           []byte
	InputLength   int64
	OutputDigest  string
}

// runParse parses the (possibly truncated) generated WAV and fingerprints it.
func runParse(stream *fixture.Stream) func(*dag.Node, map[string]*dag.Node) (any, error) {
	return func(n *dag.Node, deps map[string]*dag.Node) (any, error) {
		raw := stream.TruncatedWAV()
		w, err := media.ParseWAV(raw)
		out := &parseOutput{
			WAV:         w,
			BlockAlign:  stream.Config.BlockAlign(),
			SampleRate:  stream.Config.SampleRate,
			FramesPerPkt: stream.Config.PacketSamples,
		}
		if w != nil {
			out.InputDigest = MustDigest(w.PCM)
		}
		var me *clock.MediaError
		if errors.As(err, &me) && me.Code == clock.ErrTruncated {
			out.Truncated, out.TruncMissing = true, me.Missing
			return out, nil // surviving prefix is usable; truncation is reported
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

// runRepair drives the clock Repairer over every received packet.
func runRepair(stream *fixture.Stream) func(*dag.Node, map[string]*dag.Node) (any, error) {
	return func(n *dag.Node, deps map[string]*dag.Node) (any, error) {
		parsed, ok := deps["parse"].Output.(*parseOutput)
		if !ok {
			return nil, clock.NewError(clock.ErrDependency, "parse output missing")
		}
		jumpLimit := stream.Config.ClockTicksPerPkt / 4
		rep, err := clock.NewRepairer(stream.Config.ClockTicksPerPkt, jumpLimit)
		if err != nil {
			return nil, err
		}
		var maxAbsDev float64
		for _, p := range stream.Packets {
			e := rep.Observe(p.Seq(), p.TS())
			if e.Reason == clock.ReasonDrift {
				d := clock.Tick(abs64(e.DriftTicks)).Milli()
				if d > maxAbsDev {
					maxAbsDev = d
				}
			}
		}
		if !rep.IsMonotonic() {
			return nil, clock.NewError(clock.ErrBlocked, "repaired timeline is not monotonic")
		}
		c := StageCounters{Reasons: rep.CountByReason()}
		c.PacketsReceived = int64(len(stream.Packets))
		_ = parsed
		return &repairOutput{
			Entries: rep.Entries(), Packets: stream.Packets,
			SyncError: maxAbsDev, Counters: c,
		}, nil
	}
}

// runRecover runs the jitter buffer and burst-loss recovery.
func runRecover() func(*dag.Node, map[string]*dag.Node) (any, error) {
	return func(n *dag.Node, deps map[string]*dag.Node) (any, error) {
		rep, ok := deps["repair"].Output.(*repairOutput)
		if !ok {
			return nil, clock.NewError(clock.ErrDependency, "repair output missing")
		}
		rec, err := rtp.NewRecoverer(clock.Tick(900), MaxConcealPackets)
		if err != nil {
			return nil, err
		}
		for _, p := range rep.Packets {
			if err := rec.Push(p); err != nil {
				return nil, err
			}
		}
		intervals, gaps, st := rec.Flush()
		out := &recoverOutput{Intervals: intervals, Gaps: gaps, Stats: st,
			Packets: rep.Packets}
		// A blocked gap means the missing duration cannot be inferred: the
		// timeline must not be forged. Surface it as a structured blocking
		// error so the DAG records the blocking dependency and chain.
		if st.Blocked > 0 {
			return out, clock.ErrorAt(clock.ErrBlocked, int64(st.Blocked),
				"loss burst of %d packets exceeds concealment limit %d",
				st.Blocked, MaxConcealPackets)
		}
		return out, nil
	}
}

// runIndex rebuilds the random-access index, applies corruption injection and
// sanitization, and serializes the surviving index into MP4.
func runIndex(corruptCount int) func(*dag.Node, map[string]*dag.Node) (any, error) {
	return func(n *dag.Node, deps map[string]*dag.Node) (any, error) {
		parsed, ok1 := deps["parse"].Output.(*parseOutput)
		rec, ok2 := deps["recover"].Output.(*recoverOutput)
		if !ok1 || !ok2 {
			return nil, clock.NewError(clock.ErrDependency, "upstream output missing")
		}
		inputLength := int64(len(parsed.WAV.PCM))
		// Truncation may leave fewer PCM bytes than the RTP intervals imply.
		// Clip the intervals to the frames that actually exist so the index
		// never reaches past the surviving input; the missing tail stays
		// reported as truncated bytes rather than failing the run.
		intervals, gaps := clipToInput(rec.Intervals, rec.Gaps,
			inputLength, int64(parsed.BlockAlign), int64(parsed.FramesPerPkt))
		records, err := index.Rebuild(intervals, gaps, inputLength,
			parsed.BlockAlign, parsed.FramesPerPkt)
		if err != nil {
			return nil, err
		}
		// Inject deliberately corrupt records, then sanitize so the valid
		// prefix is rebuilt and every drop is counted.
		if corruptCount > 0 {
			records = injectCorruption(records, corruptCount, inputLength)
		}
		sane, report := index.Sanitize(records, inputLength, parsed.BlockAlign)
		mp4Bytes, err := mux.WriteIndexMP4(sane, inputLength)
		if err != nil {
			return nil, err
		}
		digest := MustDigest(mp4Bytes)
		return &indexOutput{
			Records: sane, Dropped: report, MP4: mp4Bytes,
			InputLength: inputLength, OutputDigest: digest,
		}, nil
	}
}

// injectCorruption appends records that violate the index contract: some
// reach past the input, some duplicate an existing time tick, the rest have
// corrupt geometry. It never touches the valid prefix.
func injectCorruption(valid []index.Record, count int, inputLength int64) []index.Record {
	out := append([]index.Record(nil), valid...)
	base := int64(len(valid))
	for i := 0; i < count; i++ {
		rec := index.Record{ID: base + int64(i), TimeTicks: clock.Tick(1_000_000 + i),
			TimeMs: float64(1_000_000+i) / 90.0, FrameIndex: base + int64(i), Length: 8}
		switch i % 3 {
		case 0: // escapes input length
			rec.ByteOffset = inputLength + int64(i)*8
		case 1: // duplicate time tick of the first valid record
			if len(valid) > 0 {
				rec.TimeTicks, rec.TimeMs = valid[0].TimeTicks, valid[0].TimeMs
			}
			rec.ByteOffset = base*8 + int64(i)*8
		default: // negative geometry
			rec.ByteOffset, rec.FrameIndex = -1, -1
		}
		out = append(out, rec)
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// clipToInput trims recovered intervals so their total frame count never
// exceeds the PCM bytes actually present after truncation. Whole intervals
// that fit are kept unchanged; a partially covered final interval is
// shortened and its end timestamp is scaled to its retained packets.
func clipToInput(intervals []rtp.Interval, gaps []rtp.Gap,
	inputLength, blockAlign, framesPerPkt int64) ([]rtp.Interval, []rtp.Gap) {
	usableFrames := inputLength / blockAlign
	maxPackets := usableFrames / framesPerPkt
	var consumed int64
	out := make([]rtp.Interval, 0, len(intervals))
	for _, iv := range intervals {
		packets := iv.EndSeq - iv.StartSeq + 1
		remaining := maxPackets - consumed
		if remaining <= 0 {
			break
		}
		if packets <= remaining {
			out = append(out, iv)
			consumed += packets
			continue
		}
		// Shorten the final interval to the packets that have PCM backing.
		step := clock.Tick(0)
		if packets > 0 {
			step = (iv.EndTS - iv.StartTS) / clock.Tick(packets)
		}
		clipped := iv
		clipped.EndSeq = iv.StartSeq + remaining - 1
		clipped.EndTS = iv.StartTS + step*clock.Tick(remaining)
		if clipped.Concealed > remaining {
			clipped.Concealed = remaining
		}
		out = append(out, clipped)
		consumed += remaining
		break
	}
	// Gaps beyond the retained packet range are no longer relevant.
	if len(out) == 0 {
		return out, nil
	}
	lastSeq := out[len(out)-1].EndSeq
	keptGaps := make([]rtp.Gap, 0, len(gaps))
	for _, g := range gaps {
		if g.StartSeq <= lastSeq {
			keptGaps = append(keptGaps, g)
		}
	}
	return out, keptGaps
}
