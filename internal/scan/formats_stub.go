package scan

import "mediacore/internal/fault"

func buildRTPIndex(_ *Index) error {
	return fault.New(fault.UnsupportedFormat, "RTP fixture scanner not implemented in skeleton")
}
