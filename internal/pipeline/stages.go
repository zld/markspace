package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"medialab/internal/fixtures"
	"medialab/internal/ingest"
	"medialab/internal/model"
	"medialab/internal/pixel"
	"medialab/internal/planner"
	"medialab/internal/render"
	"medialab/internal/timeline"
)

// reorderAll runs the RTP stage for every track that has a stream and returns
// assembled payloads keyed by the track's local frame index.
func reorderAll(inputDir string, ds *fixtures.Dataset, window int) (model.Faults, map[model.TrackID][][]byte) {
	var warns model.Faults
	assembled := make(map[model.TrackID][][]byte)
	for tr := 1; tr <= ds.Spec.Tracks; tr++ {
		track := model.TrackID(tr)
		wire, err := fixtures.ReadRTP(inputDir, track)
		if err != nil {
			warns = append(warns, model.NewFault(model.KindIngest, model.CodePayloadCorrupt,
				track, 0, fmt.Sprintf("read RTP stream: %v", err)).RecoverableFault())
			continue
		}
		rr := ingest.Reorder(wire, ingest.ReorderConfig{Window: window})
		warns = append(warns, rr.Warnings...)
		if rr.Fatal != nil {
			// A corrupt payload stops this track only; the others keep going.
			warns = append(warns, rr.Fatal)
			continue
		}
		frames, asmWarns := ingest.Assemble(rr.Packets)
		warns = append(warns, asmWarns...)
		byFrame := make([][]byte, ds.Spec.FramesEach)
		for idx, payload := range frames {
			// Packet.Frame is the track-local position, matching the manifest's
			// 1-based sequence minus one.
			if idx >= 0 && idx < len(byFrame) {
				byFrame[idx] = payload
			}
		}
		assembled[track] = byFrame
	}
	return warns, assembled
}

// makeDecoder returns the render decoder. Frames with an RTP stream are read
// from the reassembled payload, which proves reordering delivered the frame
// the manifest declared. Tracks without a stream fall back to the source file.
func makeDecoder(inputDir string, ds *fixtures.Dataset, assembled map[model.TrackID][][]byte) func(model.Frame) (pixel.Raw, error) {
	return func(f model.Frame) (pixel.Raw, error) {
		local := localFrameIndex(ds, f)
		if payload, ok := assembled[f.Track]; ok {
			if local < 0 || local >= len(payload) || payload[local] == nil {
				return pixel.Raw{}, model.NewFault(model.KindIngest, model.CodeFrameMissing,
					f.Track, f.Sequence, "frame fragments incomplete; recording a fill frame").RecoverableFault()
			}
			return decodePayload(payload[local], f)
		}
		return decodeSource(inputDir, f)
	}
}

// localFrameIndex maps a manifest frame to its position within the track.
func localFrameIndex(ds *fixtures.Dataset, f model.Frame) int {
	return int(f.Sequence) - 1
}

// decodePayload decodes an RTP-assembled payload and proves it is the frame
// the manifest declared, so reordering cannot smuggle a different frame in.
func decodePayload(data []byte, f model.Frame) (pixel.Raw, error) {
	raw, track, seq, err := pixel.Decode(data)
	if err != nil {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodePayloadCorrupt,
			f.Track, f.Sequence, fmt.Sprintf("decode RTP payload: %v", err))
	}
	if int(track) != int(f.Track) || seq != f.Sequence {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			f.Track, f.Sequence, "RTP payload identity differs from the manifest")
	}
	return raw, nil
}

// decodeSource decodes a source frame from the manifest file. It is the fallback
// when a track has no RTP stream at all.
func decodeSource(inputDir string, f model.Frame) (pixel.Raw, error) {
	data, err := os.ReadFile(filepath.Join(inputDir, f.Path))
	if err != nil {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			f.Track, f.Sequence, fmt.Sprintf("read source: %v", err))
	}
	if got := sha256Hex(data); got != f.SHA256 {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			f.Track, f.Sequence, "source bytes changed after ingest validation")
	}
	raw, track, seq, err := pixel.Decode(data)
	if err != nil {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodePayloadCorrupt,
			f.Track, f.Sequence, fmt.Sprintf("decode source: %v", err))
	}
	if int(track) != int(f.Track) || seq != f.Sequence {
		return pixel.Raw{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			f.Track, f.Sequence, "source payload identity differs from the manifest")
	}
	return raw, nil
}

// frameSampler keeps every fps-th track position, matching the planner's
// frame-count model. fps 1 keeps everything.
func frameSampler(fps int) func(timeline.Event) bool {
	if fps <= 1 {
		return nil
	}
	return func(e timeline.Event) bool { return e.TrackIndex%fps == 0 }
}

// plannerInput describes the export work in terms the planner models from the
// source frames and the merged timeline.
func plannerInput(inputDir string, ds *fixtures.Dataset, minQuality float64) planner.Input {
	if len(ds.Frames) == 0 {
		return planner.Input{}
	}
	var total int64
	perTrack := make([]int, ds.Spec.Tracks)
	sample := ds.Frames[0]
	for _, f := range ds.Frames {
		total += int64(f.Size)
		if f.Track > 0 && int(f.Track) <= len(perTrack) {
			perTrack[int(f.Track)-1]++
		}
	}
	baseBytes := total / int64(len(ds.Frames))
	// Cost model: one decoded frame costs roughly a millisecond per 8 KiB of
	// source payload plus a fixed decode/encode overhead.
	baseCost := 1 + baseBytes/8192
	return planner.Input{
		BaseBytesPerFrame: baseBytes,
		StepBytes:         measureStepBytes(inputDir, sample, minQuality),
		BaseCostMS:        baseCost,
		TotalFrames:       len(ds.Frames),
		PerTrackFrames:    perTrack,
		// Container and manifest overhead: ftyp/moov per track plus the JSON
		// manifest itself; it scales with track count, not frame count.
		OverheadBytes: 2048*int64(ds.Spec.Tracks) + 2048,
	}
}

// plannerBudget is the deterministic search grid and envelope.
func plannerBudget(cfg Config) planner.Budget {
	return planner.Budget{
		MaxBytes: cfg.MaxBytes, MaxCostMS: cfg.BudgetMS, MinQuality: cfg.MinQuality,
		FPSChoices: []int{4, 2, 1}, QualityChoices: []float64{1, 0.875, 0.75, 0.5},
	}
}

// measureStepBytes renders one source frame at every candidate quantisation
// step and records the PNG size. The planner's analytic model is only a
// fallback; real measurements keep the byte budget honest, and measuring a
// single frame keeps planning cheap. Measurement is best-effort: a decode
// failure leaves StepBytes empty and the analytic model in charge.
func measureStepBytes(inputDir string, f model.Frame, minQuality float64) map[int]int64 {
	src, err := decodeSource(inputDir, f)
	if err != nil {
		return nil
	}
	out := make(map[int]int64, 5)
	for _, q := range []float64{1, 0.875, 0.75, 0.5, minQuality} {
		step := pixel.StepForQuality(q)
		n, err := render.MeasurePNG(pixel.Quantise(src, step))
		if err != nil {
			return nil
		}
		out[step] = n
	}
	return out
}

func faultOf(err error) *model.Fault {
	if f, ok := model.AsFault(err); ok {
		return f
	}
	return model.NewFault(model.KindInput, model.CodeManifestInvalid, 0, 0, err.Error())
}

// planFault turns a planner error into the structured fault the CLI reports,
// preserving the planner's own conflict code where one exists.
func planFault(plan *planner.Plan, err error) *model.Fault {
	if plan != nil && plan.Conflict != nil && plan.Conflict.Reason != "" {
		return model.NewFault(model.KindPlan, plan.Conflict.Reason, 0, 0, err.Error())
	}
	if f, ok := model.AsFault(err); ok {
		return f
	}
	return model.NewFault(model.KindPlan, model.CodeBudgetConflict, 0, 0, err.Error())
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
