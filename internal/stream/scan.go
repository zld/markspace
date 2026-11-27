package stream

import (
	"encoding/json"
	"os"

	perr "gamepack/internal/errors"
)

const subsystem = "stream"

// ScanFile parses a raw bitstream and its keyframe index sidecar. It returns
// a per-unit view and boundary events anchored at original byte offsets.
// Offset out-of-range, truncated units and missing index entries fail with
// the unified error model and the first locatable byte; incomplete units are
// never returned.
func ScanFile(codec Codec, streamPath, indexPath string) (*ScanResult, error) {
	if !codec.Valid() {
		return nil, perr.New(subsystem, perr.CodeInvalidEncoding, "unsupported codec %q", codec)
	}
	raw, err := os.ReadFile(streamPath)
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeInvalidEncoding,
			"cannot read stream %q: %v", streamPath, err)
	}
	idx, err := readIndex(indexPath)
	if err != nil {
		return nil, err
	}
	if idx.Codec != codec {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"index codec %q does not match stream codec %q", idx.Codec, codec)
	}

	var units []Unit
	switch codec {
	case CodecH264, CodecH265:
		units, err = scanAnnexB(codec, raw)
	case CodecAV1:
		units, err = scanOBUs(raw)
	default:
		err = perr.New(subsystem, perr.CodeInvalidEncoding, "unsupported codec %q", codec)
	}
	if err != nil {
		return nil, err
	}

	bounds, err := reconcileIndex(raw, units, idx)
	if err != nil {
		return nil, err
	}
	return &ScanResult{Codec: codec, Units: units, Boundaries: bounds}, nil
}

func readIndex(path string) (*Index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"cannot read keyframe index %q: %v", path, err)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"malformed index %q: %v", path, err).Wrap(err)
	}
	if !idx.Codec.Valid() {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"index declares unsupported codec %q", idx.Codec)
	}
	return &idx, nil
}

// EncodeIndex serializes an index in the canonical sidecar JSON layout.
func EncodeIndex(idx *Index) ([]byte, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	return b, nil
}

// DecodeIndex parses a canonical index (used by tests and the fixture tool).
func DecodeIndex(b []byte) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}
