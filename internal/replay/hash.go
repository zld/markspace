package replay

import (
	"encoding/json"
	"fmt"

	"replay/internal/fault"
	"replay/internal/scenario"
	"replay/internal/state"
)

// fixtureInputHash digests everything that determines ingest output: the
// case, every RTP datagram in input order with its offset, and every RTCP
// datagram including any declared truncation. Payload bytes are hashed,
// never stored.
func fixtureInputHash(fx scenario.Fixture) string {
	parts := []string{fx.Case, versionKey(fx.Versions)}
	for _, p := range fx.RTP {
		parts = append(parts, "rtp", p.Track, fmtInt(p.Offset), state.HashBytes(p.Bytes))
	}
	for _, p := range fx.RTCP {
		parts = append(parts, "rtcp", p.Track, fmtInt(p.Offset), fmtInt(int64(p.Truncate)), state.HashBytes(p.Bytes))
	}
	return state.HashStrings(parts...)
}

// graphInputHash digests the declared track graph.
func graphInputHash(fx scenario.Fixture) string {
	data, err := json.Marshal(fx.Tracks)
	if err != nil {
		return state.HashStrings(fx.SortedTracks()...)
	}
	return state.HashStrings(string(data))
}

// assetSpecHash digests the declared assets, including content keys, so a
// content change invalidates mapping and everything after it.
func assetSpecHash(fx scenario.Fixture) string {
	var parts []string
	for _, a := range fx.Assets {
		parts = append(parts, a.Version, a.Track, fmtInt(a.DurationNS),
			fmtInt(a.TrackOffsetNS), a.ContentKey, fmtInt(int64(a.Salt)))
	}
	return state.HashStrings(parts...)
}

// canvasKey captures the requested mapping window and version order.
func canvasKey(fx scenario.Fixture) string {
	return state.HashStrings(fmtInt(fx.WantInterval[0]), fmtInt(fx.WantInterval[1]), versionKey(fx.Versions))
}

// stageError attaches a stage name to any error while preserving
// structured context.
func stageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	if fe, ok := fault.As(err); ok {
		if fe.Stage == "" {
			fe.Stage = stage
		}
		return fe
	}
	return fault.New(fault.KindInput, "stage failed").At(stage).Wrap(err)
}

func fmtInt(v int64) string {
	return fmt.Sprintf("%d", v)
}

func versionKey(versions []string) string {
	data, _ := json.Marshal(versions)
	return string(data)
}

// stageInputHash derives one stage's digest from its direct inputs plus
// the upstream stage's output digest.
func (e *Engine) stageInputHash(stage string) (string, error) {
	switch stage {
	case StageIngest:
		return fixtureInputHash(e.Fixture), nil
	case StageTimeline:
		return state.HashStrings(StageIngest, e.outputs[StageIngest]), nil
	case StageGraph:
		return graphInputHash(e.Fixture), nil
	case StageMap:
		return state.HashStrings(e.outputs[StageGraph], assetSpecHash(e.Fixture), canvasKey(e.Fixture)), nil
	case StageMerge:
		return state.HashStrings(e.outputs[StageMap]), nil
	case StageManifest:
		return state.HashStrings(e.outputs[StageMerge]), nil
	}
	return "", fault.New(fault.KindArgument, "unknown stage "+stage).At("replay")
}
