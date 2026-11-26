package fixture

import (
	"context"
	"os"
	"path/filepath"

	"regress/internal/scan"
)

// StreamSet is one fully generated, locally reproducible input pair.
type StreamSet struct {
	Codec       string
	StreamPath  string
	IndexPath   string
	Scan        *scan.ScanResult
	Index       *scan.KeyIndex
	TimeDen     uint64
	SegmentLen  int // keyframe interval; a keyframe lands exactly on each boundary
}

// DefaultSpec returns the deterministic synthesis parameters shared by the
// demo and the tests: 48 frames at 24 fps with a keyframe every 12 frames,
// i.e. four keyframes that coincide exactly with the four segment borders.
func DefaultSpec(codec string) Spec {
	return Spec{Codec: codec, Width: 176, Height: 144, FPS: 24, Frames: 48, GOP: 12}
}

// Build renders a clean stream and its matching keyframe index into dir.
func Build(ctx context.Context, dir string, spec Spec) (*StreamSet, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path, err := GenerateElementary(ctx, dir, spec)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	res, err := scan.ScanElementary(spec.Codec, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	timeDen := uint64(spec.FPS)
	idx := scan.BuildKeyIndex(res, timeDen)
	raw, err := scan.MarshalKeyIndex(idx)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(dir, spec.Codec+".kidx")
	if err := os.WriteFile(indexPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &StreamSet{
		Codec:      spec.Codec,
		StreamPath: path,
		IndexPath:  indexPath,
		Scan:       res,
		Index:      idx,
		TimeDen:    timeDen,
		SegmentLen: spec.GOP,
	}, nil
}

// CorruptIndex rewrites one of four index entries so its offset points at a
// non-keyframe access unit, reproducing the corrupt-index regression case.
// The returned path is a sibling file; the original index stays intact.
func CorruptIndex(set *StreamSet, entryNum int) (string, error) {
	idx := &scan.KeyIndex{TimeDen: set.Index.TimeDen,
		Entries: append([]scan.IndexEntry(nil), set.Index.Entries...)}
	if entryNum < 0 || entryNum >= len(idx.Entries) {
		entryNum = 1
	}
	// Point the entry at the access unit immediately after its keyframe.
	targetOrdinal := idx.Entries[entryNum].FrameOrdinal + 1
	if targetOrdinal >= len(set.Scan.AccessUnits) {
		targetOrdinal = 0
	}
	idx.Entries[entryNum].Offset = set.Scan.AccessUnits[targetOrdinal].Offset
	raw, err := scan.MarshalKeyIndex(idx)
	if err != nil {
		return "", err
	}
	out := filepath.Join(filepath.Dir(set.IndexPath), set.Codec+"-corrupt.kidx")
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// TruncateTail produces a deterministically damaged stream whose final unit
// is incomplete. Annex B streams get a dangling 00 00 01 start code with no
// following NAL byte; AV1 streams get a final temporal-delimiter OBU whose
// LEB128 size claims bytes that run past EOF. Both shapes are rejected with
// fault.CodeTruncatedTail.
func TruncateTail(set *StreamSet) (string, error) {
	raw, err := os.ReadFile(set.StreamPath)
	if err != nil {
		return "", err
	}
	var out []byte
	if set.Codec == scan.CodecAV1 {
		out = append(append([]byte(nil), raw...), 0x12, 0x05)
	} else {
		out = append(append([]byte(nil), raw...), 0x00, 0x00, 0x01)
	}
	path := filepath.Join(filepath.Dir(set.StreamPath), set.Codec+"-trunc"+filepath.Ext(set.StreamPath))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
