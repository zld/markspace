package export

import (
	"sort"

	"github.com/pion/rtp"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
)

func payloadFromReport(data []byte, sr stream.SampleReport, inputID, inputSHA string) ([]byte, *fault.Error) {
	if !sr.RTPOK || sr.AbsoluteOffset < 0 || sr.Size <= 0 {
		return nil, fault.E(fault.StageExport, fault.CodeUnreadableSample, "sample was not scanned as readable",
			fault.WithInput(inputID, inputSHA), fault.WithTrack(sr.TrackID),
			fault.WithOffset(sr.AbsoluteOffset, int64(sr.Size)))
	}
	end := sr.AbsoluteOffset + int64(sr.Size)
	if end > int64(len(data)) {
		return nil, fault.E(fault.StageExport, fault.CodeOffsetOutOfRange, "sample exceeds input",
			fault.WithInput(inputID, inputSHA), fault.WithOffset(int64(len(data)), end-int64(len(data))))
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(data[sr.AbsoluteOffset:end]); err != nil {
		return nil, fault.Wrap(fault.StageExport, fault.CodeReadFailed, err, "reparse RTP payload")
	}
	return pkt.Payload, nil
}

func indexesForTrack(m *mp4.Manifest, trackID uint32) []int {
	var out []int
	for _, s := range m.Samples {
		if s.TrackID == trackID {
			out = append(out, s.Index)
		}
	}
	sort.Ints(out)
	return out
}
