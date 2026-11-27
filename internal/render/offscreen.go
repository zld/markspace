// Package render performs the offscreen export: it decodes the selected source
// frames, quantises them, writes one PNG per frame and one MP4 clip per track,
// and records every hash and parameter in the artifact manifest. Export is
// deterministic: identical input and parameters produce byte-identical files.
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"

	"medialab/internal/artifact"
	"medialab/internal/fixtures/mp4seg"
	"medialab/internal/model"
	"medialab/internal/pixel"
	"medialab/internal/timeline"
	"medialab/internal/worker"
)

// Options configures one export.
type Options struct {
	OutDir   string
	Plan     *artifact.Params
	Workers  int
	CacheCap int64
	Fill     string
	// Keep selects which merged events become output frames; nil keeps all.
	Keep func(timeline.Event) bool
	// Decode turns a manifest frame into its raw image. Injectable for tests.
	Decode func(f model.Frame) (pixel.Raw, error)
}

// Result is the in-memory outcome before the manifest is re-read.
type Result struct {
	Manifest artifact.Manifest
	Faults   model.Faults
	Warnings model.Faults
	Segments map[model.TrackID]string
}

type frameRecord struct {
	entry artifact.Frame
	data  []byte
}

type trackOutput struct {
	track  model.TrackID
	width  int
	height int
	frames []frameRecord
}

// Export runs the whole offscreen pipeline for one timeline.
func Export(ctx context.Context, tl *timeline.Timeline, opt Options) (*Result, error) {
	if err := validateOptions(&opt); err != nil {
		return nil, err
	}
	for _, sub := range []string{"frames", "segments"} {
		if err := os.MkdirAll(filepath.Join(opt.OutDir, sub), 0o755); err != nil {
			return nil, model.NewFault(model.KindRender, model.CodeArtifactMismatch, 0, 0,
				fmt.Sprintf("create output: %v", err))
		}
	}

	selected := make([]timeline.Event, 0, len(tl.Events))
	for _, e := range tl.Events {
		if opt.Keep == nil || opt.Keep(e) {
			selected = append(selected, e)
		}
	}
	if len(selected) == 0 {
		return nil, model.NewFault(model.KindRender, model.CodeManifestInvalid, 0, 0,
			"no frames selected for export")
	}

	jobs := make([]worker.Job, len(selected))
	for i, e := range selected {
		e, rel := e, frameRelPath(e)
		jobs[i] = worker.Job{
			Track: e.Frame.Track, Ordinal: i, Key: rel,
			// Cache estimate: raw RGB plane plus the encoded PNG payload.
			Size: int64(e.Frame.Width*e.Frame.Height*3 + e.Frame.Size + 4096),
			Run: func(ctx context.Context) ([]byte, error) {
				return renderFrame(ctx, e, rel, opt)
			},
		}
	}
	report := worker.Run(ctx, worker.Config{
		Workers: opt.Workers, MaxCacheBytes: opt.CacheCap, FillPolicy: opt.Fill,
	}, jobs)

	res := &Result{Segments: make(map[model.TrackID]string)}
	res.Faults = append(res.Faults, report.Faults...)
	res.Warnings = append(res.Warnings, tl.Warnings...)

	byOrdinal := make(map[int]worker.Outcome, len(report.Outcomes))
	for _, o := range report.Outcomes {
		byOrdinal[o.Job.Ordinal] = o
	}

	tracks := make(map[model.TrackID]*trackOutput)
	var trackOrder []model.TrackID
	var manifestFrames []artifact.Frame

	for i, e := range selected {
		t, ok := tracks[e.Frame.Track]
		if !ok {
			t = &trackOutput{track: e.Frame.Track, width: e.Frame.Width, height: e.Frame.Height}
			tracks[e.Frame.Track] = t
			trackOrder = append(trackOrder, e.Frame.Track)
		}
		o := byOrdinal[i]
		switch {
		case o.Skipped:
			// The track was cancelled earlier; still emit a fill so partial
			// output stays complete and deterministic.
			rec, err := writeFill(opt.OutDir, e)
			if err != nil {
				res.Faults = append(res.Faults, toFault(err, e.Frame))
				continue
			}
			t.frames = append(t.frames, rec)
			manifestFrames = append(manifestFrames, rec.entry)
		case o.Fault != nil && !o.Fault.Recoverable:
			// The job failed hard; record a fill so the track stays complete and
			// let the fault propagate to the caller.
			rec, err := writeFill(opt.OutDir, e)
			if err != nil {
				res.Faults = append(res.Faults, toFault(err, e.Frame))
				continue
			}
			t.frames = append(t.frames, rec)
			manifestFrames = append(manifestFrames, rec.entry)
		case o.Fault != nil:
			rec, err := writeFill(opt.OutDir, e)
			if err != nil {
				res.Faults = append(res.Faults, toFault(err, e.Frame))
				continue
			}
			t.frames = append(t.frames, rec)
			manifestFrames = append(manifestFrames, rec.entry)
			res.Warnings = append(res.Warnings, o.Fault)
		default:
			rec, err := recordFromBytes(o.Product, e)
			if err != nil {
				res.Faults = append(res.Faults, toFault(err, e.Frame))
				continue
			}
			t.frames = append(t.frames, rec)
			manifestFrames = append(manifestFrames, rec.entry)
		}
	}

	sort.SliceStable(trackOrder, func(i, j int) bool { return trackOrder[i] < trackOrder[j] })
	segments, segFaults := writeSegments(opt.OutDir, trackOrder, tracks)
	res.Faults = append(res.Faults, segFaults...)
	for _, s := range segments {
		res.Segments[s.Track] = s.Path
	}

	res.Faults.Sort()
	res.Warnings.Sort()
	res.Manifest = artifact.Manifest{
		Version:  artifact.Version,
		Params:   *opt.Plan,
		Frames:   manifestFrames,
		Segments: segments,
		Warnings: res.Warnings,
		Faults:   res.Faults,
	}
	finalise(&res.Manifest)
	return res, nil
}

func validateOptions(opt *Options) error {
	if opt.OutDir == "" {
		return model.NewFault(model.KindRender, model.CodeArtifactMismatch, 0, 0, "no output directory")
	}
	if opt.Plan == nil {
		return model.NewFault(model.KindRender, model.CodeArtifactMismatch, 0, 0, "no render parameters")
	}
	if opt.Workers < 1 {
		opt.Workers = 1
	}
	if opt.CacheCap <= 0 {
		opt.CacheCap = 64 << 20
	}
	if opt.Fill == "" {
		opt.Fill = "hold-last"
	}
	if opt.Decode == nil {
		return model.NewFault(model.KindRender, model.CodeArtifactMismatch, 0, 0, "no frame decoder")
	}
	return nil
}

// frameRelPath is the deterministic relative path of an exported frame.
func frameRelPath(e timeline.Event) string {
	return fmt.Sprintf("frames/t%d-%06d.png", e.Frame.Track, e.Frame.Sequence)
}

func renderFrame(ctx context.Context, e timeline.Event, rel string, opt Options) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, "render cancelled")
	}
	src, err := opt.Decode(e.Frame)
	if err != nil {
		return nil, toFault(err, e.Frame)
	}
	if src.W != e.Frame.Width || src.H != e.Frame.Height {
		return nil, model.NewFault(model.KindRender, model.CodeSizeBounds,
			e.Frame.Track, e.Frame.Sequence,
			fmt.Sprintf("decoded %dx%d differs from declared %dx%d", src.W, src.H, e.Frame.Width, e.Frame.Height))
	}
	step := opt.Plan.QuantStep
	if step < 1 {
		step = 1
	}
	data, err := encodePNG(toNRGBA(pixel.Quantise(src, step)))
	if err != nil {
		return nil, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, fmt.Sprintf("encode PNG: %v", err))
	}
	if err := writeFileAtomic(filepath.Join(opt.OutDir, rel), data); err != nil {
		return nil, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, fmt.Sprintf("write PNG: %v", err))
	}
	return data, nil
}

func toNRGBA(r pixel.Raw) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, r.W, r.H))
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			i := (y*r.W + x) * 3
			img.SetNRGBA(x, y, color.NRGBA{R: r.Pix[i], G: r.Pix[i+1], B: r.Pix[i+2], A: 255})
		}
	}
	return img
}

// DecodePNG is the inverse used by the verifier; keeping PNG I/O in one place
// guarantees both sides handle colour models identically.
func DecodePNG(b []byte) (pixel.Raw, error) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return pixel.Raw{}, err
	}
	bounds := img.Bounds()
	r := pixel.Raw{W: bounds.Dx(), H: bounds.Dy(), Pix: make([]byte, bounds.Dx()*bounds.Dy()*3)}
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			c := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			i := (y*r.W + x) * 3
			r.Pix[i], r.Pix[i+1], r.Pix[i+2] = c.R, c.G, c.B
		}
	}
	return r, nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// recordFromBytes derives the full frame record by re-decoding the artefact.
func recordFromBytes(pngBytes []byte, e timeline.Event) (frameRecord, error) {
	raw, err := DecodePNG(pngBytes)
	if err != nil {
		return frameRecord{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, fmt.Sprintf("re-read artefact: %v", err))
	}
	stats, err := pixel.Measure(raw)
	if err != nil {
		return frameRecord{}, err
	}
	loud, err := pixel.LoudnessPeakDBFS(raw)
	if err != nil {
		return frameRecord{}, err
	}
	entry := artifact.Frame{
		Track: e.Frame.Track, Sequence: e.Frame.Sequence, PTS: int64(e.Frame.PTS),
		SourceSHA256: e.Frame.SHA256, SHA256: sha256Hex(pngBytes), Path: frameRelPath(e),
		Bytes: int64(len(pngBytes)), Width: raw.W, Height: raw.H,
		Stats: stats, LoudnessDBFS: loud,
	}
	return frameRecord{entry: entry, data: pngBytes}, nil
}

func writeFill(outDir string, e timeline.Event) (frameRecord, error) {
	raw := fillImage(e.Frame.Width, e.Frame.Height, e.Frame.Track, e.Frame.Sequence)
	data, err := encodePNG(toNRGBA(raw))
	if err != nil {
		return frameRecord{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, fmt.Sprintf("encode fill: %v", err))
	}
	if err := writeFileAtomic(filepath.Join(outDir, frameRelPath(e)), data); err != nil {
		return frameRecord{}, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
			e.Frame.Track, e.Frame.Sequence, fmt.Sprintf("write fill: %v", err))
	}
	rec, err := recordFromBytes(data, e)
	if err != nil {
		return frameRecord{}, err
	}
	rec.entry.Fill = true
	return rec, nil
}

// fillImage paints the deterministic fill pattern for a recoverable gap: a
// grey ramp keyed by track and sequence. Two runs agree byte for byte, and
// different gaps stay distinguishable in the pixel statistics.
func fillImage(w, h int, track model.TrackID, seq uint32) pixel.Raw {
	r := pixel.Raw{W: w, H: h, Pix: make([]byte, w*h*3)}
	base := (int(track)*37 + int(seq)*11) % 64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(96 + (base+x+y*3)%48)
			i := (y*w + x) * 3
			r.Pix[i], r.Pix[i+1], r.Pix[i+2] = v, v, v
		}
	}
	return r
}

// writeSegments writes one MP4 per track in track-id order, using nanosecond
// ticks so stts reproduces the shared time base exactly.
func writeSegments(outDir string, order []model.TrackID, tracks map[model.TrackID]*trackOutput) ([]artifact.Segment, model.Faults) {
	var out []artifact.Segment
	var faults model.Faults
	for _, tr := range order {
		t := tracks[tr]
		if len(t.frames) == 0 {
			continue
		}
		seg := mp4seg.Segment{TimeScale: 1_000_000_000, Width: t.width, Height: t.height}
		for _, f := range t.frames {
			seg.Samples = append(seg.Samples, mp4seg.Sample{Data: f.data, Ticks: f.entry.PTS})
		}
		var buf bytes.Buffer
		if err := mp4seg.Write(&buf, seg); err != nil {
			faults = append(faults, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
				tr, 0, fmt.Sprintf("write segment: %v", err)))
			continue
		}
		rel := fmt.Sprintf("segments/track-%d.mp4", tr)
		if err := writeFileAtomic(filepath.Join(outDir, rel), buf.Bytes()); err != nil {
			faults = append(faults, model.NewFault(model.KindRender, model.CodeArtifactMismatch,
				tr, 0, fmt.Sprintf("write segment: %v", err)))
			continue
		}
		first := t.frames[0].entry.PTS
		last := t.frames[len(t.frames)-1].entry.PTS
		out = append(out, artifact.Segment{
			Track: tr, Path: rel, SHA256: sha256Hex(buf.Bytes()), Bytes: int64(buf.Len()),
			Samples: len(t.frames), Width: t.width, Height: t.height,
			TimeScale: seg.TimeScale, DurationNS: last - first,
			FirstPTS: first, LastPTS: last,
		})
	}
	return out, faults
}

// finalise fills the aggregate summary from the per-frame records. It is
// deliberately computed from artefact bytes, not from source metadata.
func finalise(m *artifact.Manifest) {
	sum := artifact.Summary{Tracks: len(m.Segments), Frames: len(m.Frames)}
	sum.PeakLoudnessDBFS = pixel.LoudnessFloorDBFS
	var sr, sg, sb, sq float64
	for _, f := range m.Frames {
		if f.Fill {
			sum.FillFrames++
		}
		sum.TotalBytes += f.Bytes
		sr += f.Stats.MeanR
		sg += f.Stats.MeanG
		sb += f.Stats.MeanB
		sq += f.Stats.Variance
		if f.LoudnessDBFS > sum.PeakLoudnessDBFS {
			sum.PeakLoudnessDBFS = f.LoudnessDBFS
		}
	}
	for _, s := range m.Segments {
		sum.TotalBytes += s.Bytes
	}
	if n := float64(len(m.Frames)); n > 0 {
		sum.MeanR, sum.MeanG, sum.MeanB = sr/n, sg/n, sb/n
		sum.Variance = sq / n
	}
	h := sha256.New()
	for _, f := range m.Frames {
		h.Write([]byte(f.SHA256))
	}
	for _, s := range m.Segments {
		h.Write([]byte(s.SHA256))
	}
	sum.RunHash = hex.EncodeToString(h.Sum(nil))
	m.Summary = sum
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func toFault(err error, f model.Frame) *model.Fault {
	if fault, ok := model.AsFault(err); ok {
		return fault
	}
	return model.NewFault(model.KindRender, model.CodeArtifactMismatch,
		f.Track, f.Sequence, err.Error())
}
