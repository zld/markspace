package mediacore

import (
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// RemuxOptions configures lossless remuxing.
type RemuxOptions struct {
	Map TrackMapConfig
	// Validate re-reads the written file and compares sample payload hashes
	// and timestamps with the source. It is on by default.
	Validate bool
}

// RemuxResult reports what was written.
type RemuxResult struct {
	SourcePath string       `json:"source_path"`
	OutputPath string       `json:"output_path"`
	TrackMap   *TrackMap    `json:"track_map"`
	Tracks     []RemuxTrack `json:"tracks"`
	OutputSize int64        `json:"output_size"`
	SameMedia  bool         `json:"same_media"`
}

// RemuxTrack reports per-track preservation.
type RemuxTrack struct {
	SourceID   uint32 `json:"source_id"`
	TargetID   uint32 `json:"target_id"`
	Samples    int    `json:"samples"`
	SourceHash string `json:"source_hash"`
	OutputHash string `json:"output_hash"`
	Preserved  bool   `json:"preserved"`
}

// SampleRange is a contiguous run of samples to copy, used by segment-aware
// streaming writes. Start is inclusive, End exclusive.
type SampleRange struct {
	Start int
	End   int
}

// Remux copies media from src to dst without decoding or re-encoding:
// sample payload bytes, DTS, duration and composition offsets are preserved
// exactly, and only the container's track numbering and box layout change.
func Remux(src *Container, dst string, opts RemuxOptions) (*RemuxResult, error) {
	if src == nil {
		return nil, fail(KindInvalidConfig, "remux", "nil container")
	}
	return RemuxRanges(src, dst, nil, opts)
}

// RemuxRanges is Remux restricted to selected source sample ranges per
// track. A nil map means "all samples". Ranges are validated against the
// source sample table; invalid ranges fail before the output is opened.
func RemuxRanges(src *Container, dst string, ranges map[uint32][]SampleRange, opts RemuxOptions) (*RemuxResult, error) {
	if src == nil {
		return nil, fail(KindInvalidConfig, "remux", "nil container")
	}
	tm, err := BuildTrackMap(src, opts.Map)
	if err != nil {
		return nil, err
	}
	if err := validateTrackMapAgainstSource(src, tm); err != nil {
		return nil, err
	}
	selection, err := resolveRanges(src, ranges)
	if err != nil {
		return nil, err
	}

	// Source track IDs are unique, so mux track order is fully determined
	// by the source table; keep it independent of range map iteration.
	selection, err = sortedSelection(selection)
	if err != nil {
		return nil, err
	}
	muxTracks, err := buildMuxTracks(src, tm, selection)
	if err != nil {
		return nil, err
	}
	ftypLen := int64(len(ftypBytesFor(src.Brand)))
	payloadBase := ftypLen + 8 // ftyp box + mdat header
	chunks, payloadEnd := planMuxLayout(muxTracks, payloadBase)
	payloadLen := payloadEnd - payloadBase
	moov, err := buildMoov(muxTracks, chunks, src.MovieTimescale, src.MovieDuration)
	if err != nil {
		return nil, err
	}
	payload, err := writePayload(src, dst, seedTmp(dst, "payload"), selection, muxTracks, payloadLen)
	if err != nil {
		return nil, err
	}
	defer os.Remove(payload)

	if err := assembleFile(dst, src, payload, moov); err != nil {
		return nil, err
	}
	res := &RemuxResult{
		SourcePath: src.Path,
		OutputPath: dst,
		TrackMap:   tm,
	}
	st, err := os.Stat(dst)
	if err != nil {
		return nil, wrapErr(KindIO, "remux", err)
	}
	res.OutputSize = st.Size()
	for i := range muxTracks {
		res.Tracks = append(res.Tracks, RemuxTrack{
			SourceID:  muxTracks[i].SourceID,
			TargetID:  muxTracks[i].ID,
			Samples:   len(muxTracks[i].Samples),
			Preserved: len(muxTracks[i].RawEntry) > 0,
		})
	}
	if opts.Validate {
		if err := validateRemux(src, dst, res); err != nil {
			return nil, err
		}
		res.SameMedia = true
	}
	return res, nil
}

// ftypBytesFor builds a minimal ftyp. Brand is carried over so QuickTime
// files stay QuickTime files and ISO files stay ISO.
func ftypBytesFor(brand string) []byte {
	if len(brand) != 4 {
		brand = "isom"
	}
	b, err := buildFtyp(brand)
	if err != nil {
		return nil
	}
	return b
}

func seedTmp(dst, suffix string) string { return dst + "." + suffix + ".tmp" }

type payloadPlan struct {
	srcTrack *Track
	ranges   []int
	sizes    []int64
}

// writePayload streams source samples into a temporary payload file in the
// layout the writer expects, returning its path.
func writePayload(src *Container, dst, tmpPath string, selection map[uint32][]int, muxTracks []muxTrack, wantLen int64) (string, error) {
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", wrapErr(KindIO, "remux", err)
	}
	defer f.Close()
	var written int64
	for _, mt := range muxTracks {
		srcTrack := src.TrackByID(mt.SourceID)
		reader, err := NewSampleReader(src.Path, srcTrack)
		if err != nil {
			return "", err
		}
		indices := selection[mt.SourceID]
		if indices == nil {
			for i := range srcTrack.Samples {
				indices = append(indices, i)
			}
		}
		for _, i := range indices {
			n, err := reader.ReadSample(i, f)
			if err != nil {
				reader.Close()
				return "", err
			}
			written += n
		}
		reader.Close()
	}
	if written != wantLen {
		return "", fail(KindInternal, "remux", "payload length %d does not match planned %d", written, wantLen)
	}
	if err := f.Close(); err != nil {
		return "", wrapErr(KindIO, "remux", err)
	}
	return tmpPath, nil
}

// assembleFile concatenates ftyp + mdat + moov, copying payload without
// buffering it.
func assembleFile(dst string, src *Container, payloadPath string, moov []byte) error {
	out, err := os.Create(dst)
	if err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	defer out.Close()
	ftyp := ftypBytesFor(src.Brand)
	if len(ftyp) == 0 {
		return fail(KindContainer, "remux", "cannot build ftyp")
	}
	if _, err := out.Write(ftyp); err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	st, err := os.Stat(payloadPath)
	if err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	hdr := boxHeader(mp4.BoxTypeMdat(), st.Size()+8)
	if _, err := out.Write(hdr); err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	payload, err := os.Open(payloadPath)
	if err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	defer payload.Close()
	if _, err := io.Copy(out, payload); err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	if _, err := out.Write(moov); err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	if err := out.Close(); err != nil {
		return wrapErr(KindIO, "remux", err)
	}
	return nil
}

func short(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10]
}

// Describe renders the remux result.
func (r *RemuxResult) Describe() string {
	out := fmt.Sprintf("remux %s -> %s (%d bytes, same_media=%t)", r.SourcePath, r.OutputPath, r.OutputSize, r.SameMedia)
	for _, t := range r.Tracks {
		out += fmt.Sprintf("\n  %d -> %d (%d samples, hash=%s)", t.SourceID, t.TargetID, t.Samples, short(t.SourceHash))
	}
	return out
}
