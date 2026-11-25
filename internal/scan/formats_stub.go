package scan

import "mediacore/internal/fault"

func buildMP4Index(_ *Index) error {
	return fault.New(fault.UnsupportedFormat, "MP4/MOV scanner not implemented in skeleton")
}

func buildRTPIndex(_ *Index) error {
	return fault.New(fault.UnsupportedFormat, "RTP fixture scanner not implemented in skeleton")
}
