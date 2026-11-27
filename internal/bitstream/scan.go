package bitstream

import (
	"sort"

	"mediacore/internal/fault"
)

// Scan parses an elementary stream in the named codec and validates its
// parameter sets. Every returned error is a *fault.Fault carrying the codec,
// the first offending byte offset and a stable code.
func Scan(codec Codec, data []byte) (*Stream, error) {
	if len(data) == 0 {
		return nil, fault.New(fault.CodeScanTruncatedHeader, 0, string(codec), "empty elementary stream")
	}
	var frames []Frame
	var paramOK bool
	var err error
	switch codec {
	case CodecH264, CodecH265:
		frames, paramOK, err = scanAnnexB(codec, data)
	default:
		return nil, fault.Newf(fault.CodeScanUnsupportedCodec, string(codec), "unsupported codec %q", codec)
	}
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fault.New(fault.CodeScanNoFrames, 0, string(codec), "no coded frames found")
	}
	if !paramOK {
		return nil, fault.New(fault.CodeScanMissingParameter, frames[0].Offset, string(codec), "no sequence parameter set precedes the first frame")
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].Offset < frames[j].Offset })
	for i := range frames {
		frames[i].Index = i
	}
	return &Stream{Codec: codec, Size: int64(len(data)), Frames: frames, ParameterOK: paramOK}, nil
}
