package demux

import (
	"bytes"

	"medpipe/internal/fault"
)

// DetectFormat sniffs the supported container format.
func DetectFormat(data []byte) (string, *fault.Error) {
	if len(data) >= 1 && data[0] == 0x47 {
		return "mpeg-ts", nil
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return "mp4", nil
	}
	return "", fault.New(fault.ErrUnmappableTrack, "demux.detect", 0, "unknown container: expected MPEG-TS sync byte or MP4 ftyp box")
}

// DemuxBytes routes either MPEG-TS bytes or an in-memory MP4 file. MP4 inputs
// are normally read through ReadMP4File because sample offsets require a
// seeker.
func DemuxBytes(data []byte) (*Media, []fault.Event, *fault.Error) {
	format, err := DetectFormat(data)
	if err != nil {
		return nil, nil, err
	}
	if format == "mpeg-ts" {
		return DemuxTS(data)
	}
	return nil, []fault.Event{{Code: fault.ErrUnmappableTrack, Severity: "fatal", Stage: "demux.detect", Message: "use ReadMP4File for seekable MP4 input"}},
		fault.New(fault.ErrUnmappableTrack, "demux.detect", 0, "MP4 input must be opened as a seekable file")
}
