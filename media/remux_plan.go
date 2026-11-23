package media

import (
	"io"
	"path/filepath"
	"strings"
)

// RemuxResult reports what a remux pass produced and the evidence collected.
type RemuxResult struct {
	OutputPath  string            `json:"output_path"`
	OutputSize  int64             `json:"output_size"`
	Tracks      []TrackEvidence   `json:"tracks"`
	Segments    []Segment         `json:"segments"`
	SourceHash  map[uint32]string `json:"source_hash"`
	OutputHash  map[uint32]string `json:"output_hash"`
	SampleCount int               `json:"sample_count"`
}

// TrackEvidence is the per-track comparison between input and output.
type TrackEvidence struct {
	SourceID    uint32    `json:"source_id"`
	TargetID    uint32    `json:"target_id"`
	Kind        TrackKind `json:"kind"`
	Language    string    `json:"language"`
	SampleCount uint32    `json:"sample_count"`
	SourceSHA   string    `json:"source_sha256"`
	OutputSHA   string    `json:"output_sha256"`
	ByteMatch   bool      `json:"byte_match"`
	DTSMatch    bool      `json:"dts_match"`
	CTSMatch    bool      `json:"cts_match"`
	SyncMatch   bool      `json:"sync_match"`
}

// RemuxOptions controls the lossless remux.
type RemuxOptions struct {
	OutDir         string // temp and output must live under this directory
	MovieTimescale uint32 // 0 keeps the probe's movie timescale
	Brand          string // container brand; empty keeps probe brand
}

// ValidateOutPath rejects paths that escape the allowed output directory.
func ValidateOutPath(dir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", NewError(ClassInvalidArgument, "path", 0, "empty output path")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", WrapError(ClassInvalidArgument, "path", 0, err, "bad output directory")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", WrapError(ClassInvalidArgument, "path", 0, err, "bad output path")
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return "", WrapError(ClassInvalidArgument, "path", 0, err, "path not relative to output directory")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", NewError(ClassInvalidArgument, "path", 0, "output %q escapes output directory %q", path, dir)
	}
	return absPath, nil
}

func hashSampleStream(src io.ReadSeeker, st *SampleTable) (string, error) {
	seg := Segment{Index: 0, TrackID: st.TrackID, First: 0, Count: st.Count}
	return HashSegment(src, st, seg)
}

// remuxTrackPlan captures everything needed to emit one output track.
type remuxTrackPlan struct {
	src       *TrackInfo
	stsd      []byte
	timescale uint32
	lang      [3]byte
	kind      TrackKind
	durs      []uint32
	ctsOff    []int64
	sync      []bool
	sizes     []uint32
	edits     []EditEntry
	width     uint16
	height    uint16
	channels  uint16
	rate      uint32
	duration  uint64
}

func planTrack(src io.ReadSeeker, ti *TrackInfo) (*remuxTrackPlan, error) {
	rt := ti.Raw
	if rt == nil || rt.Mdhd == nil {
		return nil, NewError(ClassUnsupported, "remux.plan", 0, "track %d lacks raw metadata", ti.TrackID).WithTrack(ti.TrackID)
	}
	// copy the sample description verbatim so codec configuration is preserved
	stsdPayload, err := readBoxPayload(src, rt.Stsd)
	if err != nil {
		return nil, err
	}
	plan := &remuxTrackPlan{
		src: ti, stsd: stsdPayload, timescale: rt.Mdhd.Timescale,
		lang: rt.Mdhd.Language, kind: KindOf(ti),
		durs: ti.Table.Durations, ctsOff: ti.Table.CTS,
		sync: ti.Table.Sync, sizes: ti.Table.Sizes,
		duration: ti.Table.DTS[ti.Table.Count-1] + uint64(ti.Table.Durations[ti.Table.Count-1]),
	}
	for _, e := range ti.Edits {
		plan.edits = append(plan.edits, e)
	}
	if rt.Tkhd != nil {
		plan.width = rt.Tkhd.GetWidthInt()
		plan.height = rt.Tkhd.GetHeightInt()
	}
	if rt.Codec == "mp4a" {
		plan.channels = 2
		plan.rate = plan.timescale
	}
	return plan, nil
}

func readBoxPayload(r io.ReadSeeker, b Box) ([]byte, error) {
	if b.Size < b.Header || b.Size > MaxInputSize {
		return nil, NewError(ClassBoxBounds, "remux.read", b.Offset, "bad box %q", b.Type)
	}
	buf := make([]byte, b.Size-b.Header)
	if _, err := r.Seek(b.Offset+b.Header, io.SeekStart); err != nil {
		return nil, WrapError(ClassIO, "remux.seek", b.Offset, err, "seek %q", b.Type)
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, WrapError(ClassTruncated, "remux.read", b.Offset, err, "short %q", b.Type)
	}
	return buf, nil
}
