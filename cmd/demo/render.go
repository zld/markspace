package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"replay/internal/cmaf"
	"replay/internal/events"
	"replay/internal/manifest"
	"replay/internal/model"
	"replay/internal/planner"
	"replay/internal/worker"
)

// buildJob assembles the worker job that renders one clip. ph is the
// content-aware parameter hash shared with the manifest.
func buildJob(s setInfo, o options, segs []cmaf.Segment, win events.Window, cand *planner.Candidate, outDir, outPath, ph string) worker.Job {
	return worker.Job{
		ID:         s.name + "/" + o.event,
		ParamsHash: ph,
		OutputPath: outPath,
		Run: func(ctx context.Context) (worker.Result, error) {
			return renderClip(ctx, s, segs, win, cand, outDir, outPath)
		},
	}
}

// computeParamsHash returns a deterministic hash of every parameter that can
// change the rendered clip, including a byte fingerprint of the init segment
// and the exact fragments used. Any input repair therefore invalidates a
// stale worker marker instead of reusing pre-repair bytes.
func computeParamsHash(s setInfo, o options, win events.Window, cand *planner.Candidate, segs []cmaf.Segment) (string, error) {
	h := sha256.New()
	if err := hashFileInto(h, join(s.dir, "init.mp4")); err != nil {
		return "", err
	}
	for _, sg := range segs {
		if err := hashFileInto(h, sg.Frag.Path); err != nil {
			return "", err
		}
	}
	fp := hex.EncodeToString(h.Sum(nil))
	params := fmt.Sprintf("v2;set=%s;event=%s;ss=%d;t=%d;vb=%d;ab=%d;ar=%d;ch=%d;conv=%t;nseg=%d;fp=%s",
		s.name, o.event, win.Start.Milliseconds(), win.Dur().Milliseconds(),
		cand.VideoBitrate, cand.AudioBitrate, cand.Audio.SampleRate, cand.Audio.Channels,
		cand.Color.Convert, len(segs), fp)
	sum := sha256.Sum256([]byte(params))
	return hex.EncodeToString(sum[:]), nil
}

// hashFileInto streams a file into h.
func hashFileInto(h hash.Hash, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	return nil
}

// renderClip concatenates init + the window's fragments and re-encodes the
// event window at the planned bitrate. Encode settings are deterministic
// (single thread, CBR, bitexact) so identical inputs and parameters
// produce identical outputs.
func renderClip(ctx context.Context, s setInfo, segs []cmaf.Segment, win events.Window, cand *planner.Candidate, outDir, outPath string) (worker.Result, error) {
	if err := ensureDir(outDir); err != nil {
		return worker.Result{}, err
	}
	tmp, err := os.MkdirTemp("", "replay-cut-")
	if err != nil {
		return worker.Result{}, err
	}
	defer os.RemoveAll(tmp)

	srcs := []string{join(s.dir, "init.mp4")}
	for _, seg := range segs {
		srcs = append(srcs, seg.Frag.Path)
	}
	concat := join(tmp, "concat.mp4")
	if err := concatFiles(concat, srcs...); err != nil {
		return worker.Result{}, err
	}
	// Remux to normalize the absolute tfdt timeline to zero; direct seeks
	// on concatenated fMP4 are unreliable.
	remux := join(tmp, "remux.mp4")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error",
		"-i", concat, "-c", "copy", remux).CombinedOutput(); err != nil {
		return worker.Result{}, fmt.Errorf("ffmpeg remux: %w: %s", err, out)
	}
	relStart := win.Start
	if len(segs) > 0 {
		relStart = win.Start - segs[0].Start
		if relStart < 0 {
			relStart = 0
		}
	}

	args := []string{
		"-y", "-loglevel", "error",
		"-ss", secStr(relStart),
		"-i", remux,
		"-t", secStr(win.Dur()),
		"-c:v", "libx264",
		"-b:v", fmt.Sprint(cand.VideoBitrate),
		"-minrate", fmt.Sprint(cand.VideoBitrate),
		"-maxrate", fmt.Sprint(cand.VideoBitrate),
		"-bufsize", fmt.Sprint(cand.VideoBitrate / 2),
		"-x264-params", "nal-hrd=cbr",
		"-threads", "1",
		"-pix_fmt", "yuv420p",
		"-vf", outputFilter(cand),
		"-c:a", "aac", "-b:a", fmt.Sprint(cand.AudioBitrate),
		"-ar", fmt.Sprint(cand.Audio.SampleRate),
		"-movflags", "+write_colr",
		"-fflags", "+bitexact", "-flags", "+bitexact",
	}
	args = append(args, outPath)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return worker.Result{}, fmt.Errorf("ffmpeg cut: %w: %s", err, out)
	}

	sum, err := fileSHA256(outPath)
	if err != nil {
		return worker.Result{}, err
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return worker.Result{}, err
	}
	return worker.Result{
		SHA256:     sum,
		SizeBytes:  st.Size(),
		DurationMS: win.Dur().Milliseconds(),
	}, nil
}

// outputFilter builds the render filter chain: color conversion when the
// plan requires it, and explicit bt709 tagging via setparams (output-side
// -color_* options do not reliably reach the colr box).
func outputFilter(cand *planner.Candidate) string {
	vf := ""
	if cand.Color.Convert {
		vf = "scale=out_color_matrix=bt709,"
	}
	return vf + "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709"
}

// buildManifest assembles the delivery manifest for one produced clip. ph is
// the content-aware parameter hash shared with the worker job.
func buildManifest(s setInfo, o options, win events.Window, tl *cmaf.Timeline, cand *planner.Candidate, res worker.Result, rep *model.Report, nfrags int, ph string) *manifest.Manifest {
	segs := tl.SegmentsIn(win.Start, win.End)
	segInfos := make([]manifest.SegmentInfo, 0, len(segs))
	for _, sg := range segs {
		segInfos = append(segInfos, manifest.SegmentInfo{
			Path:    filepath.Base(sg.Frag.Path),
			Seq:     sg.Frag.Seq,
			StartMS: sg.Start.Milliseconds(),
			DurMS:   sg.Dur.Milliseconds(),
		})
	}
	missing := make([][2]int64, 0, len(tl.Missing))
	for _, m := range tl.Missing {
		missing = append(missing, [2]int64{m.Start.Milliseconds(), m.End.Milliseconds()})
	}
	bitrate := int64(0)
	if res.DurationMS > 0 {
		bitrate = res.SizeBytes * 8 * 1000 / res.DurationMS
	}
	return &manifest.Manifest{
		Version: manifest.Version,
		Set:     s.name,
		Event:   o.event,
		Window:  manifest.Window{StartMS: win.Start.Milliseconds(), EndMS: win.End.Milliseconds()},
		Timeline: manifest.TimelineInfo{
			Fragments:       nfrags,
			SeqGaps:         tl.SeqGaps,
			Duplicates:      tl.Duplicates,
			TfdtRegressions: tl.Regressions,
			MissingMS:       missing,
			Segments:        segInfos,
		},
		Audio: manifest.AudioInfo{
			InputChannels:    cand.Audio.Input.Channels,
			InputSampleRate:  cand.Audio.Input.SampleRate,
			OutputChannels:   cand.Audio.Channels,
			OutputSampleRate: cand.Audio.SampleRate,
			Layout:           cand.Audio.Layout,
			Resampled:        cand.Audio.Resample,
		},
		Video: manifestVideoInfo(cand),
		Output: manifest.Output{
			Path:       filepath.Base(res.OutputPath),
			SHA256:     res.SHA256,
			SizeBytes:  res.SizeBytes,
			DurationMS: res.DurationMS,
			Bitrate:    bitrate,
		},
		Params: manifest.Params{
			WindowBeforeMS: o.before.Milliseconds(),
			WindowAfterMS:  o.after.Milliseconds(),
			TargetBitrate:  o.bitrate,
			Workers:        o.workers,
			ParamsHash:     ph,
		},
		IssueCounts: rep.Counts(),
	}
}

// manifestVideoInfo maps the color plan into the manifest.
func manifestVideoInfo(cand *planner.Candidate) manifest.VideoInfo {
	return manifest.VideoInfo{
		ColorSource: cand.Color.Source,
		ColorTarget: cand.Color.Target,
		Converted:   cand.Color.Convert,
		Note:        cand.Color.Note,
	}
}
