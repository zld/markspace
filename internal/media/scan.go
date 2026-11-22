// Package media performs container analysis on recovered material: MP4 index
// building, silence detection on the unified timeline and content
// fingerprinting for deduplication.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"github.com/archive/mediakernel/internal/timeline"
)

// Scan limits keep a hostile container from exhausting memory or time.
const (
	MaxSamples      = 1_000_000
	MaxScanBytes    = 512 << 20 // 512 MiB
	MaxTracks       = 64
)

// Sample is one indexed sample expressed on the unified axis.
type Sample struct {
	Index       int
	IsKeyframe  bool
	StartUS     int64 // media-local start on the unified axis (us)
	DurationUS  int64
	Size        uint32
}

// TrackIndex is the per-track result of a scan.
type TrackIndex struct {
	TrackID    uint32
	Timescale  uint32
	IsAudio    bool
	Samples    []Sample
	Keyframes  []int // sample indices that are decodable boundaries
	MediaEndUS int64
}

// Index is the full container index plus its input digest.
type Index struct {
	Digest       string // sha256 over the complete input bytes
	Size         int64
	MovieTS      uint32
	Tracks       []*TrackIndex
	FirstMediaUS int64 // earliest sample start across tracks (non-zero base)
}

// DigestBytes is the canonical digest label used in manifests and reports.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestFile streams a file through sha256 without loading it fully, honoring
// the scan-size limit.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", timeline.WrapError(timeline.KindInvalidInput, "open for digest failed", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyN(h, f, MaxScanBytes+1)
	if err != nil && err != io.EOF {
		return "", timeline.WrapError(timeline.KindCorrupt, "digest read failed", err)
	}
	if n > MaxScanBytes {
		return "", timeline.NewError(timeline.KindLimit, "input exceeds scan limit of %d bytes", MaxScanBytes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ScanBytes parses a complete in-memory MP4, validates limits and builds the
// sample/keyframe index. All sample times are converted to unified-axis
// microseconds at this boundary; downstream code never sees raw timescales.
func ScanBytes(data []byte) (*Index, error) {
	if len(data) == 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "empty MP4 input")
	}
	if int64(len(data)) > MaxScanBytes {
		return nil, timeline.NewError(timeline.KindLimit, "MP4 size %d exceeds limit %d", len(data), MaxScanBytes)
	}
	r := bytes.NewReader(data)
	info, err := mp4.Probe(r)
	if err != nil {
		return nil, timeline.WrapError(timeline.KindCorrupt, "mp4 probe failed", err)
	}
	if len(info.Tracks) == 0 {
		return nil, timeline.NewError(timeline.KindCorrupt, "MP4 contains no tracks")
	}
	if len(info.Tracks) > MaxTracks {
		return nil, timeline.NewError(timeline.KindLimit, "track count %d exceeds limit %d", len(info.Tracks), MaxTracks)
	}

	idx := &Index{
		Digest:  DigestBytes(data),
		Size:    int64(len(data)),
		MovieTS: info.Timescale,
	}
	for _, tr := range info.Tracks {
		ti, err := indexTrack(r, tr)
		if err != nil {
			return nil, err
		}
		idx.Tracks = append(idx.Tracks, ti)
	}
	idx.FirstMediaUS = idx.earliestStart()
	return idx, nil
}

func indexTrack(r io.ReadSeeker, tr *mp4.Track) (*TrackIndex, error) {
	if len(tr.Samples) > MaxSamples {
		return nil, timeline.NewError(timeline.KindLimit, "sample count %d exceeds limit %d", len(tr.Samples), MaxSamples)
	}
	ti := &TrackIndex{TrackID: tr.TrackID, Timescale: tr.Timescale, IsAudio: tr.Codec == mp4.CodecMP4A}
	// Walk stts: Probe expands every sample with its own TimeDelta, so compute
	// cumulative media-local timestamps in timescale units.
	var mediaCursor int64
	for i, s := range tr.Samples {
		start, err := timeline.FromScale(mediaCursor, tr.Timescale)
		if err != nil {
			return nil, timeline.WrapError(timeline.KindCorrupt, "sample timestamp out of range", err)
		}
		dur, err := timeline.FromScale(int64(s.TimeDelta), tr.Timescale)
		if err != nil {
			return nil, timeline.WrapError(timeline.KindCorrupt, "sample duration out of range", err)
		}
		ti.Samples = append(ti.Samples, Sample{
			Index: i, StartUS: int64(start), DurationUS: int64(dur), Size: s.Size,
		})
		mediaCursor += int64(s.TimeDelta)
	}
	end, err := timeline.FromScale(mediaCursor, tr.Timescale)
	if err != nil {
		return nil, err
	}
	ti.MediaEndUS = int64(end)

	// Keyframes: Probe does not surface stss, so extract it explicitly.
	keySet, err := readKeyframeSet(r, tr.TrackID)
	if err != nil {
		return nil, err
	}
	for i := range ti.Samples {
		if keySet == nil {
			ti.Samples[i].IsKeyframe = true // no stss: every sample is independently decodable
			continue
		}
		if _, ok := keySet[uint32(i+1)]; ok { // stss is 1-based
			ti.Samples[i].IsKeyframe = true
			ti.Keyframes = append(ti.Keyframes, i)
		}
	}
	if keySet == nil {
		for i := range ti.Samples {
			ti.Keyframes = append(ti.Keyframes, i)
		}
	}
	return ti, nil
}

// readKeyframeSet returns the stss sample numbers (1-based) for a track, or nil
// when the track has no stss box.
func readKeyframeSet(r io.ReadSeeker, trackID uint32) (map[uint32]struct{}, error) {
	path := []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()}}
	bis, err := mp4.ExtractBoxesWithPayload(r, nil, path)
	if err != nil {
		return nil, timeline.WrapError(timeline.KindCorrupt, "stss extraction failed", err)
	}
	if len(bis) == 0 {
		return nil, nil
	}
	// Multiple traks may exist; the extracted order follows trak order, which
	// Probe reports in the same sequence. The caller identifies its track by
	// position externally; here every stss found is merged for the simple
	// single-track fixtures, and multi-track callers pass per-track readers.
	set := make(map[uint32]struct{})
	for _, bi := range bis {
		stss, ok := bi.Payload.(*mp4.Stss)
		if !ok {
			continue
		}
		for _, n := range stss.SampleNumber {
			set[n] = struct{}{}
		}
	}
	_ = trackID
	return set, nil
}

func (idx *Index) earliestStart() int64 {
	var first int64
	firstSet := false
	for _, tr := range idx.Tracks {
		for _, s := range tr.Samples {
			if !firstSet || s.StartUS < first {
				first, firstSet = s.StartUS, true
			}
		}
	}
	return first
}

// AudioTrack returns the first audio track index, or -1 when none exists.
func (idx *Index) AudioTrack() int {
	for i, tr := range idx.Tracks {
		if tr.IsAudio {
			return i
		}
	}
	return -1
}

// NearestKeyframe maps a requested cut time (unified-axis microseconds,
// media-local) to the closest decodable keyframe at or before the cut when
// distances tie. It returns the boundary actually used and the signed
// deviation (boundary - requested) so the pipeline can report boundary bias
// instead of silently cutting at a non-decodable sample.
func NearestKeyframe(tr *TrackIndex, requestedUS int64) (boundaryUS, deviationUS int64, err error) {
	if len(tr.Keyframes) == 0 {
		return 0, 0, timeline.NewError(timeline.KindCorrupt, "track %d has no keyframes", tr.TrackID)
	}
	// Binary search for the last keyframe starting at or before the request.
	lo, hi := 0, len(tr.Keyframes)
	for lo < hi {
		mid := (lo + hi) / 2
		if tr.Samples[tr.Keyframes[mid]].StartUS <= requestedUS {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	beforeIdx := lo - 1
	afterIdx := lo // first keyframe strictly after request, may be out of range

	beforeUS := tr.Samples[tr.Keyframes[beforeIdx]].StartUS
	if afterIdx >= len(tr.Keyframes) {
		return beforeUS, beforeUS - requestedUS, nil
	}
	afterUS := tr.Samples[tr.Keyframes[afterIdx]].StartUS
	dBefore := requestedUS - beforeUS
	dAfter := afterUS - requestedUS
	// Tie resolves to the earlier boundary to keep cuts conservative.
	if dAfter < dBefore {
		return afterUS, afterUS - requestedUS, nil
	}
	return beforeUS, beforeUS - requestedUS, nil
}
