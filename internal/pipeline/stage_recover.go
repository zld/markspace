package pipeline

import (
	"mediacore/internal/bitstream"
	"mediacore/internal/fault"
)

// stageRecover packetises the stream, applies deterministic delay and
// reordering, and recovers it. Recovered packet counts are read back from the
// delivered output rather than assumed.
func stageRecover(rep *Report, opts Options, rawBytes []byte, stream *bitstream.Stream) ([]byte, error) {
	packets, delayed, err := packetizeStream(rawBytes, stream, opts)
	if err != nil {
		return nil, err
	}
	rep.LostPackets = len(delayed)
	rec, err := recoverPackets(packets, opts)
	if err != nil {
		return nil, err
	}
	rep.Packets = rec.Packets
	rep.MissingPackets = rec.Missing
	rep.RecoveredPackets = countDelivered(rec, delayed)
	if rep.Packets < 1 {
		rep.Decision = "reject"
		rep.DecisionReason = "no packets survived recovery"
		rep.computeDigest()
		return nil, fault.Newf(fault.CodeRTPGapUnrecovered, "", "all packets were lost")
	}
	return rec.Bytes(), nil
}
