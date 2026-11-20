package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"

	"mp4seek/internal/avc"
	"mp4seek/internal/bmff"
	"mp4seek/internal/index"
	"mp4seek/internal/report"
	"mp4seek/internal/timeline"
)

// presentedSample pairs a sample with its movie-time placement.
type presentedSample struct {
	index.Sample
	MovieTicks int64
	MovieMs    int64
}

// prepareTrack builds the classic sample table for trackID, verifies the
// codec is unencrypted AVC, and maps samples to movie time.
func prepareTrack(r *bmff.Reader, mv *movie, trackID uint32) (*trackInfo, *index.Table, int, []presentedSample, error) {
	ti, err := mv.track(trackID)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	if !ti.HasStbl {
		return nil, nil, 0, nil, fmt.Errorf("%w: track %d has no sample table", errUnsupported, trackID)
	}
	tbl, err := index.BuildClassic(r, ti.Stbl)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	if tbl.Entry.Encrypted {
		return nil, nil, 0, nil, fmt.Errorf("%w: track %d is encrypted", errUnsupported, trackID)
	}
	if !tbl.Entry.IsAVC() {
		return nil, nil, 0, nil, fmt.Errorf("%w: track %d codec %q is not AVC", errUnsupported, trackID, tbl.Entry.Format)
	}
	if tbl.Entry.AvcC == nil {
		return nil, nil, 0, nil, fmt.Errorf("%w: track %d missing avcC", bmff.ErrCorrupt, trackID)
	}
	lengthSize, err := avc.ParseAvcC(tbl.Entry.AvcC)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	mapper := timeline.NewMapper(ti.Edits)
	var out []presentedSample
	for _, s := range tbl.Samples {
		movieTicks, ok := mapper.MediaToMovie(s.PTS)
		if !ok {
			continue // not presented by any edit
		}
		out = append(out, presentedSample{
			Sample:     s,
			MovieTicks: movieTicks,
			MovieMs:    movieTicks * 1000 / int64(mv.Timescale),
		})
	}
	return ti, tbl, lengthSize, out, nil
}

// msToTicks converts milliseconds to movie timescale ticks, guarding
// against overflow.
func msToTicks(ms int64, timescale uint32) (int64, error) {
	if ms < 0 {
		return 0, fmt.Errorf("negative time %d", ms)
	}
	ts := int64(timescale)
	if ts <= 0 || ms > (1<<62)/ts {
		return 0, fmt.Errorf("time %d ms overflows timescale %d", ms, timescale)
	}
	return ms * ts / 1000, nil
}

// sampleInfo caches classification and hash for one sample.
type sampleInfo struct {
	cls avc.Classification
	sha string
}

// classifyUpTo reads and classifies every listed sample, recording
// stss/IDR conflicts as diagnostics on stderr.
func classifyUpTo(r *bmff.Reader, samples []presentedSample, lengthSize int, stderr io.Writer) (map[int]sampleInfo, error) {
	infos := make(map[int]sampleInfo, len(samples))
	for _, ps := range samples {
		data, err := r.ReadSample(ps.Offset, ps.Size)
		if err != nil {
			return nil, err
		}
		cls, err := avc.Classify(data, lengthSize)
		if err != nil {
			return nil, fmt.Errorf("sample %d: %w", ps.Index, err)
		}
		sum := sha256.Sum256(data)
		infos[ps.Index] = sampleInfo{cls: cls, sha: hex.EncodeToString(sum[:])}
		isIDR := cls.Type == avc.IDR
		switch {
		case ps.Key && !isIDR:
			fmt.Fprintf(stderr, "diagnostic: sample=%d marked sync in stss but NAL classifies as %s; excluded from anchors\n",
				ps.Index, cls.Type)
		case !ps.Key && isIDR:
			fmt.Fprintf(stderr, "diagnostic: sample=%d is IDR but not listed in stss\n", ps.Index)
		}
	}
	return infos, nil
}

func cmdSeek(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "input MP4 file")
	track := fs.Uint("track", 0, "track ID")
	atMs := fs.Int64("at-ms", -1, "target movie time in milliseconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *track == 0 || *atMs < 0 {
		fs.Usage()
		return fmt.Errorf("seek: --input, --track and --at-ms are required")
	}
	r, f, err := openInput(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	mv, err := loadMovie(r)
	if err != nil {
		return err
	}
	ti, _, lengthSize, presented, err := prepareTrack(r, mv, uint32(*track))
	if err != nil {
		return err
	}
	target, err := msToTicks(*atMs, mv.Timescale)
	if err != nil {
		return err
	}
	if len(presented) == 0 || target < presented[0].MovieTicks {
		return fmt.Errorf("%w: %d ms precedes the presented range", errOutOfRange, *atMs)
	}
	if d := int64(mv.Duration); d > 0 && target > d {
		return fmt.Errorf("%w: %d ms past movie duration", errOutOfRange, *atMs)
	}

	// Classify every sample presented at or before the target.
	var upto []presentedSample
	for _, ps := range presented {
		if ps.MovieTicks <= target {
			upto = append(upto, ps)
		}
	}
	infos, err := classifyUpTo(r, upto, lengthSize, stderr)
	if err != nil {
		return err
	}

	// Anchor: nearest verified IDR (stss-consistent) at or before target.
	anchor := -1
	for i, ps := range upto {
		if ps.Key && infos[ps.Index].cls.Type == avc.IDR {
			anchor = i
		}
	}
	if anchor < 0 {
		return fmt.Errorf("%w: %d ms", errNoAnchor, *atMs)
	}
	a := upto[anchor]
	ai := infos[a.Index]
	w := func(kv ...any) error { return report.KV(stdout, kv...) }
	if err := w("requested_ms", *atMs); err != nil {
		return err
	}
	if err := w("anchor_sample", a.Index); err != nil {
		return err
	}
	if err := w("pts_ticks", a.PTS, "timescale", ti.Timescale,
		"movie_pts_ms", a.MovieMs, "type", ai.cls.Type, "sha256", ai.sha); err != nil {
		return err
	}
	// Per-sample records from the anchor to the target (decode order).
	for _, ps := range presented {
		if ps.Index <= a.Index || ps.MovieTicks > target {
			continue
		}
		info, ok := infos[ps.Index]
		if !ok {
			continue
		}
		if err := w("sample", ps.Index, "pts_ticks", ps.PTS,
			"movie_pts_ms", ps.MovieMs, "type", info.cls.Type, "sha256", info.sha); err != nil {
			return err
		}
	}
	return nil
}
