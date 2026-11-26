package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/index"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/timeline"
)

// RepReport is one representation's verification row.
type RepReport struct {
	RepID        string  `json:"rep_id"`
	TargetBPS    int64   `json:"target_bps"`
	ActualBPS    int64   `json:"actual_bps"`
	DeviationPct float64 `json:"deviation_pct"`
	PSNRDB       float64 `json:"psnr_db"`
	MaxAlignMS   float64 `json:"max_align_ms"`
}

// Report is the full verify output printed by the CLI.
type Report struct {
	Representations  []RepReport `json:"representations"`
	Recovered        int         `json:"recovered"`
	DuplicatePublish int         `json:"duplicate_publish"`
	ManifestSHA256   string      `json:"manifest_sha256"`
}

// publishedManifest is the deterministic output manifest document.
type publishedManifest struct {
	Version     int                `json:"version"`
	InputRoot   string             `json:"input_root"`
	Segments    map[string]string  `json:"segments"` // repID/idx -> store path
	Artifacts   []index.Artifact   `json:"artifacts"`
	Recovered   int                `json:"recovered"`
}

// stagePublish assembles the output manifest and atomically replaces it,
// only after every artifact is committed. No partial manifest is ever left
// at the final path.
func (r *Runner) stagePublish() (*Report, error) {
	if r.journal.IsStageDone(index.StagePublish) && r.cfg.Resume {
		return r.buildReport()
	}
	st := r.journal.State()

	doc := publishedManifest{
		Version:   1,
		InputRoot: r.cfg.InputDir,
		Segments:  map[string]string{},
		Recovered: st.Recovered,
	}
	for _, a := range st.Artifacts {
		doc.Artifacts = append(doc.Artifacts, *a)
		key := a.RepID + "/" + itoa(a.SegmentIdx)
		doc.Segments[key] = a.StorePath
	}
	raw, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return nil, clock.New(clock.KindIO, "publish", "marshal: %v", err)
	}
	sum := sha256.Sum256(raw)
	manifestSHA := hex.EncodeToString(sum[:])

	finalPath := filepath.Join(r.cfg.OutputDir, "published.json")
	if err := writeAtomic(finalPath, raw); err != nil {
		return nil, err
	}
	if err := r.journal.MarkPublished(manifestSHA); err != nil {
		return nil, err
	}
	return r.buildReport()
}

// buildReport computes bitrate, PSNR and alignment rows from verified media.
func (r *Runner) buildReport() (*Report, error) {
	rep := &Report{}
	st := r.journal.State()
	rep.Recovered = st.Recovered
	rep.DuplicatePublish = st.DuplicatePublish
	rep.ManifestSHA256 = st.ManifestSHA

	refFrame, err := r.loadReferenceFrame()
	if err != nil {
		return nil, err
	}
	maxAlign, err := timelineMaxAlign(r.timelines)
	if err != nil {
		return nil, err
	}

	// Aggregate bytes/duration per representation across its timeline.
	for i := range r.prim.Representations {
		pr := &r.prim.Representations[i]
		tl := r.timelines[pr.ID]
		if tl == nil {
			continue
		}
		var totalBytes int64
		var totalDur = clock.FromInt(0)
		// Report the worst (minimum) non-infinite frame PSNR: identical
		// frames are +Inf and must not mask noise on the overlap frames.
		worstPSNR := math.Inf(1)
		var psnrN int
		for _, item := range r.verified {
			if item.repID != pr.ID {
				continue
			}
			totalBytes += item.info.Size
			d, derr := item.info.DurationRat(48000)
			if derr != nil {
				return nil, derr
			}
			totalDur, err = totalDur.Add(d)
			if err != nil {
				return nil, err
			}
			// PSNR against the reference gradient for overlap gray frames.
			// Only the leading w*h bytes are the frame; trailing bytes are
			// deterministic bitrate padding inside the mdat.
			payload := item.info.MdatPayload
			need := r.frameW * r.frameH
			if len(payload) < need {
				return nil, clock.New(clock.KindCorruptMedia, "publish",
					"%s: mdat %d bytes smaller than frame %d",
					item.seg.URL, len(payload), need)
			}
			frame, ferr := media.FrameFromPayload(payload[:need],
				r.frameW, r.frameH)
			if ferr != nil {
				return nil, ferr
			}
			ps, perr := media.PSNR(refFrame, frame)
			if perr != nil {
				return nil, perr
			}
			if !math.IsInf(ps, 1) && ps < worstPSNR {
				worstPSNR = ps
			}
			psnrN++
		}
		br, err := media.Bitrate(totalBytes, totalDur)
		if err != nil {
			return nil, err
		}
		br = br.WithTarget(pr.BandwidthBPS)
		row := RepReport{
			RepID:        pr.ID,
			TargetBPS:    br.TargetBPS,
			ActualBPS:    br.ActualBPS,
			DeviationPct: br.DeviationPct,
			MaxAlignMS:   ratToMS(maxAlign),
			PSNRDB:       worstPSNR,
		}
		_ = psnrN
		rep.Representations = append(rep.Representations, row)
	}
	return rep, nil
}

func (r *Runner) loadReferenceFrame() (*media.GrayFrame, error) {
	if r.input.Frames.Reference == "" {
		return media.GradientFrame(r.frameW, r.frameH), nil
	}
	path, err := manifest.SafePath(r.cfg.InputDir, r.input.Frames.Reference)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, clock.New(clock.KindIO, "publish", "read ref frame: %v", err)
	}
	return media.FrameFromPayload(raw, r.frameW, r.frameH)
}

// timelineMaxAlign wraps the timeline alignment helper and returns ms.
func timelineMaxAlign(tls map[string]*timeline.Timeline) (clock.Rat, error) {
	return timeline.MaxAlignMs(tls)
}

func ratToMS(r clock.Rat) float64 {
	// Exact rational -> float only at the presentation boundary.
	return float64(r.Num) / float64(r.Den)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
