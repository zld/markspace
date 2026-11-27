package pipeline

import (
	"time"

	"github.com/example/mediad/internal/audio"
	"github.com/example/mediad/internal/cache"
	"github.com/example/mediad/internal/checkpoint"
	"github.com/example/mediad/internal/codec"
	"github.com/example/mediad/internal/model"
	"github.com/example/mediad/internal/mp4"
	"github.com/example/mediad/internal/report"
	"github.com/example/mediad/internal/rtp"
)

// Input is one material's ordered RTP datagrams plus identity.
type Input struct {
	MaterialID string
	CodecName  string
	SourceRate uint32
	Channels   int
	Datagrams  []struct {
		Data   []byte
		Offset int64
	}
}

// Runner holds the shared working directories and processing parameters.
type Runner struct {
	Params     Params
	Cache      *cache.Cache
	Checkpoint *checkpoint.Store
}

// Result pairs the report item with the muxed artifact (if any).
type Result struct {
	Item    report.Item
	Artifact []byte
}

// Run processes one material end to end. Ordering errors (sequence conflict)
// isolate the material; codec errors propagate as stable model errors.
func (r *Runner) Run(in Input) (Result, error) {
	item := report.Item{MaterialID: in.MaterialID, Codec: in.CodecName}

	order, err := rtp.Order(in.Datagrams)
	if err != nil {
		return Result{Item: item}, err
	}
	item.TotalPackets = order.TotalInput
	item.LostPackets = order.LostCount
	item.DuplicatePackets = order.DupCount
	item.ConflictCount = len(order.Conflicts)
	for _, rg := range order.LossRanges {
		item.AnomalyRanges = append(item.AnomalyRanges,
			report.AnomalyRange{From: rg.From, To: rg.To})
	}
	if order.Isolated {
		item.Isolated = true
		item.Status = report.StatusIsolated
		item.Deviations = append(item.Deviations,
			"sequence conflict: material isolated")
		return Result{Item: item}, nil
	}

	// Codec parse: malformed packets fail with stage/seq/offset.
	var parser codec.Parser
	if in.CodecName == "opus" {
		parser = codec.NewOpus(in.SourceRate)
	} else {
		parser = codec.NewAAC(in.SourceRate)
	}
	parsed, err := parseAll(parser, order.Packets)
	if err != nil {
		return Result{Item: item}, err
	}

	// Cache addressing covers input digest, source and every processing spec.
	inputDigest := rtpDigestHex(order)
	spec := cache.Spec{
		SourceID:       in.MaterialID,
		InputDigest:    inputDigest,
		Codec:          in.CodecName,
		SourceRate:     in.SourceRate,
		SourceChannels: uint8(in.Channels),
		TargetRate:     r.Params.TargetRate,
		Layout:         string(r.Params.Layout),
		LUFS:           r.Params.LUFS,
		TruePeak:       r.Params.TruePeak,
	}
	key := cache.Key(spec)

	start := time.Now()
	artifact, hit, err := r.cachedOrCompute(key, spec, inputDigest, parsed, in, &item)
	elapsed := time.Since(start)
	item.CacheHit = hit
	if err != nil {
		return Result{Item: item}, err
	}

	// Record checkpoint after a successful, atomically written stage.
	_ = r.recordCheckpoint(in.MaterialID, inputDigest, item, artifact)

	item = r.finalizeItem(item, artifact, elapsed)
	return Result{Item: item, Artifact: artifact}, nil
}

// cachedOrCompute returns a cached artifact when digests re-verify, otherwise
// recomputes and stores it.
func (r *Runner) cachedOrCompute(key string, spec cache.Spec, inputDigest string,
	parsed []parsedPacket, in Input, item *report.Item) ([]byte, bool, error) {

	// A live recomputation is needed; attempt cache validation first.
	artifact, loud, err := r.compute(parsed, in, item)
	if err != nil {
		return nil, false, err
	}
	contentDigest := cache.DigestPayload(artifact)
	if r.Cache != nil {
		if existing, gerr := r.Cache.Get(key, inputDigest, contentDigest); gerr != nil {
			// Mismatch: invalidate and continue with the fresh result.
			_ = r.Cache.Invalidate(key)
		} else if existing != nil {
			return existing.Payload, true, nil
		}
		_ = r.Cache.Put(key, cache.Entry{
			Spec: spec, SourceDigest: inputDigest,
			ContentDigest: contentDigest, Payload: artifact,
		})
	}
	_ = loud
	return artifact, false, nil
}

// compute runs resample -> layout -> loudness -> mux with the quality gates.
func (r *Runner) compute(parsed []parsedPacket, in Input, item *report.Item) ([]byte, audio.LoudnessResult, error) {
	pcm := synthPCM(parsed, in.SourceRate, in.Channels)
	item.InputSamples = uint64(pcm.Frames)

	resampler := audio.NewResampler(in.SourceRate, r.Params.TargetRate, in.Channels)
	out, err := resampler.Process(pcm)
	if err != nil {
		return nil, audio.LoudnessResult{}, err
	}
	out.Append(resampler.Flush())

	laid, err := audio.Normalize(out, r.Params.Layout)
	if err != nil {
		return nil, audio.LoudnessResult{}, err
	}
	norm, loud, err := audio.NormalizeLoudness(laid, audio.LoudnessTarget{
		IntegratedLUFS: r.Params.LUFS,
		TruePeakDBTP:   r.Params.TruePeak,
	})
	if err != nil {
		return nil, audio.LoudnessResult{}, err
	}
	item.OutputSamples = uint64(norm.Frames)
	item.LUFS = loud.OutputLUFS
	item.TruePeakDBTP = loud.OutputTruePeak

	artifact, rep, err := mp4.Mux(norm, mp4.MuxConfig{
		Rate: r.Params.TargetRate, Channels: uint16(norm.Ch), Codec: "mp4a",
	})
	if err != nil {
		return nil, audio.LoudnessResult{}, err
	}
	item.OutputBytes = rep.Bytes
	item.AvgBitrateBps = rep.AvgBitratebps
	return artifact, loud, nil
}

func (r *Runner) recordCheckpoint(id, digest string, item report.Item, artifact []byte) error {
	if r.Checkpoint == nil {
		return nil
	}
	return r.Checkpoint.Save(checkpoint.State{
		MaterialID:     id,
		MaterialDigest: digest,
		ManifestDigest: cache.DigestPayload(artifact),
		Stages: []checkpoint.StageState{{
			Stage: model.StageMux, PacketFrom: 0,
			PacketTo:     uint64(item.OutputSamples),
			OutputDigest: cache.DigestPayload(artifact),
		}},
	})
}
