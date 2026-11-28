package gen

import (
	"bytes"

	"github.com/pion/rtp"

	"mediakernel/internal/rtpin"
)

// TraceOptions controls the synthetic transport trace.
type TraceOptions struct {
	ChunkSize  int   // payload bytes per RTP packet
	Seed       int64 // deterministic shuffle source
	DupEvery   int   // duplicate every Nth packet once; 0 disables
	LossStart  int   // index of first packet in the burst loss; -1 disables
	LossLen    int   // number of consecutive packets lost then retransmitted
	ArrivalGap int64 // base arrival spacing, ns
}

// BuildTrace segments data into RTP records carrying file offsets, then
// deterministically reorders, duplicates and (loss+retransmit) a burst of
// them. The output is a .rtp trace file readable by rtpin.ReadRecord.
func BuildTrace(data []byte, opt TraceOptions) ([]byte, error) {
	if opt.ChunkSize <= 0 {
		opt.ChunkSize = 1200
	}
	if opt.ArrivalGap <= 0 {
		opt.ArrivalGap = 100_000 // 0.1 ms -> ~12 MB/s at 1200 B
	}
	type rec struct {
		off     uint64
		payload []byte
	}
	var recs []rec
	for off := 0; off < len(data); off += opt.ChunkSize {
		end := off + opt.ChunkSize
		if end > len(data) {
			end = len(data)
		}
		recs = append(recs, rec{uint64(off), data[off:end]})
	}

	// Deterministic reorder: reverse the middle third.
	n := len(recs)
	lo, hi := n/3, 2*n/3
	for i, j := lo, hi-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}

	// Burst loss: hold back LossLen packets, retransmit them at the end.
	var lost []rec
	if opt.LossStart >= 0 && opt.LossLen > 0 && opt.LossStart+opt.LossLen <= n {
		lost = append(lost, recs[opt.LossStart:opt.LossStart+opt.LossLen]...)
		recs = append(recs[:opt.LossStart], recs[opt.LossStart+opt.LossLen:]...)
	}

	var buf bytes.Buffer
	arrival := int64(0)
	seq := uint16(0)
	emit := func(r rec) error {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seq,
				Timestamp:      uint32(arrival / 1000),
				SSRC:           0x4d454449, // "MEDI"
			},
			Payload: r.payload,
		}
		seq++
		if err := rtpin.WriteRecord(&buf, arrival, r.off, pkt); err != nil {
			return err
		}
		arrival += opt.ArrivalGap
		return nil
	}
	for i, r := range recs {
		if err := emit(r); err != nil {
			return nil, err
		}
		if opt.DupEvery > 0 && i%opt.DupEvery == 0 {
			if err := emit(r); err != nil { // duplicate: same offset, later arrival
				return nil, err
			}
		}
	}
	for _, r := range lost { // retransmission recovers the burst loss
		if err := emit(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
