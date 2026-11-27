package pipeline

import (
	"mediacore/internal/bitstream"
	"mediacore/internal/media"
)

// stageSync aligns audio and video on the shared clock.
func stageSync(rep *Report, stream *bitstream.Stream, syn media.Synthetic) error {
	res, err := synchronize(stream, syn)
	if err != nil {
		return err
	}
	rep.SyncOffsetMS = round3(res.OffsetMs())
	rep.SyncResidualMS = round3(res.ResidualMs())
	return nil
}
