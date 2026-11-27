// Package rtp decodes length-framed RTP packet traces, counts duplicates
// once, reorders packets inside a fixed window and isolates late packets
// so they never enter the transform stage.
package rtp

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"rtx/internal/model"
)

// DefaultWindow is the fixed reorder window in packets.
const DefaultWindow = 16

// Packet is one decoded RTP datagram with its provenance.
type Packet struct {
	Seq       uint16
	Timestamp uint32 // raw, never rewritten
	Offset    int64  // raw byte offset of the record in the input
	Size      int    // record size in bytes including the length prefix
	Payload   int    // RTP payload size in bytes
}

// Stats summarizes decode-level counters for the report.
type Stats struct {
	Total      int `json:"packet_total"`
	Duplicates int `json:"duplicate_count"`
	Reordered  int `json:"reordered_count"`
	Late       int `json:"late_count"`
}

// ParseResult holds the deduplicated, window-ordered packets plus every
// recoverable anomaly found while decoding.
type ParseResult struct {
	Packets   []Packet // sequence order, duplicates and late packets removed
	Anomalies []model.Anomaly
	Stats     Stats
}

// recordLen is the byte width of the big-endian length prefix framing
// each RTP packet inside a trace buffer.
const recordLen = 2

// Decode walks a trace buffer of length-prefixed RTP packets. A truncated
// final record is recoverable: preceding packets stay valid. A record
// that fails RTP unmarshalling is reported and skipped.
func Decode(buf []byte, window int, limits model.Limits) (*ParseResult, error) {
	if limits.MaxPackets <= 0 {
		limits = model.DefaultLimits()
	}
	if window <= 0 {
		window = DefaultWindow
	}
	if limits.MaxBytes > 0 && len(buf) > limits.MaxBytes {
		return nil, model.NewStructuredError(model.StageRTP, model.ReasonLimit,
			0, int64(len(buf)), "input exceeds configured byte limit",
			"reject input", nil)
	}
	res := &ParseResult{}
	var arrivals []Packet
	pos := 0
	for pos < len(buf) {
		if len(arrivals) >= limits.MaxPackets {
			return nil, model.NewStructuredError(model.StageRTP, model.ReasonLimit,
				int64(pos), int64(len(arrivals)), "packet count exceeds configured limit",
				"reject input", nil)
		}
		if pos+recordLen > len(buf) {
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Stage: model.StageRTP, Reason: model.ReasonTruncatedBlock,
				Offset: int64(pos), RawValue: int64(len(buf) - pos),
				Message:  "trailing bytes shorter than record length prefix",
				Recovery: "drop trailing bytes; keep decoded packets",
			})
			break
		}
		n := int(binary.BigEndian.Uint16(buf[pos : pos+recordLen]))
		if pos+recordLen+n > len(buf) {
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Stage: model.StageRTP, Reason: model.ReasonTruncatedBlock,
				Offset: int64(pos), RawValue: int64(n),
				Message: fmt.Sprintf("record declares %d bytes, %d remain",
					n, len(buf)-pos-recordLen),
				Recovery: "drop truncated record; keep decoded packets",
			})
			break
		}
		var p rtp.Packet
		if err := p.Unmarshal(buf[pos+recordLen : pos+recordLen+n]); err != nil {
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Stage: model.StageRTP, Reason: model.ReasonHeader,
				Offset: int64(pos), RawValue: int64(n),
				Message:  "RTP unmarshal failed: " + err.Error(),
				Recovery: "skip record; continue with next packet",
			})
			pos += recordLen + n
			continue
		}
		arrivals = append(arrivals, Packet{
			Seq: p.SequenceNumber, Timestamp: p.Timestamp,
			Offset: int64(pos), Size: recordLen + n, Payload: len(p.Payload),
		})
		pos += recordLen + n
	}
	res.Stats.Total = len(arrivals)
	res.Packets = orderPackets(arrivals, window, res)
	return res, nil
}

// orderPackets applies duplicate suppression and the fixed reorder
// window. Packets arriving behind the window are late: counted, flagged
// and excluded from the output so they cannot enter the transform.
func orderPackets(arrivals []Packet, window int, res *ParseResult) []Packet {
	seen := make(map[uint16]int) // seq -> last arrival index
	var fresh []Packet
	maxSeen := int64(-1)
	for i, p := range arrivals {
		if last, ok := seen[p.Seq]; ok && i-last < 1<<15 {
			res.Stats.Duplicates++
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Stage: model.StageRTP, Reason: model.ReasonDuplicate,
				Offset: p.Offset, RawValue: int64(p.Seq),
				Message:  fmt.Sprintf("duplicate sequence number %d counted once", p.Seq),
				Recovery: "drop duplicate; keep first occurrence",
			})
			continue
		}
		seen[p.Seq] = i
		if maxSeen >= 0 && seqDiff(uint16(maxSeen), p.Seq) < 0 {
			// Arrived behind an already-seen higher sequence number:
			// recoverable by the window unless it falls outside it.
			res.Stats.Reordered++
		}
		if maxSeen < 0 || seqDiff(uint16(maxSeen), p.Seq) > 0 {
			maxSeen = int64(p.Seq)
		}
		fresh = append(fresh, p)
	}
	// Windowed reorder: buffer up to window arrivals, then emit the
	// lowest sequence number. A packet arriving behind an already
	// emitted sequence number fell outside the window: it is late,
	// counted and excluded from the output.
	var ordered []Packet
	var buf []Packet
	lastEmit := int64(-1)
	emitMin := func() {
		best := 0
		for i := 1; i < len(buf); i++ {
			if seqDiff(buf[best].Seq, buf[i].Seq) < 0 {
				best = i
			}
		}
		ordered = append(ordered, buf[best])
		lastEmit = int64(buf[best].Seq)
		buf = append(buf[:best], buf[best+1:]...)
	}
	for _, p := range fresh {
		if lastEmit >= 0 && seqDiff(p.Seq, uint16(lastEmit)) > 0 {
			res.Stats.Late++
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Stage: model.StageRTP, Reason: model.ReasonLate,
				Offset: p.Offset, RawValue: int64(p.Seq),
				Message: fmt.Sprintf("sequence %d arrived outside reorder window %d",
					p.Seq, window),
				Recovery: "exclude late packet from transform",
			})
			continue
		}
		buf = append(buf, p)
		if len(buf) > window {
			emitMin()
		}
	}
	for len(buf) > 0 {
		emitMin()
	}
	return ordered
}

// seqDiff returns the signed forward distance from a to b on the 16-bit
// sequence number ring.
func seqDiff(a, b uint16) int {
	d := int(int32(b) - int32(a))
	if d > 1<<15 {
		d -= 1 << 16
	} else if d < -(1 << 15) {
		d += 1 << 16
	}
	return d
}
