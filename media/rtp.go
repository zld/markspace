package media

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/pion/rtp"

	"mediapack/merr"
	"mediapack/timeline"
)

// RTP log file format: repeated [uint16 LE length][marshalled RTP packet].

// WriteRTPLog writes marshalled RTP packets as a length-prefixed log.
func WriteRTPLog(path string, pkts []*rtp.Packet) error {
	var out []byte
	for _, p := range pkts {
		raw, err := p.Marshal()
		if err != nil {
			return merr.Wrap(merr.KindInternal, "demux", "", "marshal rtp", err)
		}
		var hdr [2]byte
		binary.LittleEndian.PutUint16(hdr[:], uint16(len(raw)))
		out = append(out, hdr[:]...)
		out = append(out, raw...)
	}
	return os.WriteFile(path, out, 0o644)
}

// ReadRTPLog reads a length-prefixed RTP packet log.
func ReadRTPLog(path string) ([]rtp.Packet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, merr.Wrap(merr.KindCorrupt, "demux", "", "read rtp log", err)
	}
	var pkts []rtp.Packet
	for off := 0; off < len(data); {
		if off+2 > len(data) {
			return nil, merr.Corruptf("demux", "", "rtp log truncated at record header offset %d", off)
		}
		n := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		if off+n > len(data) {
			return nil, merr.Corruptf("demux", "", "rtp log truncated at record offset %d", off)
		}
		var p rtp.Packet
		if err := p.Unmarshal(data[off : off+n]); err != nil {
			return nil, merr.Wrap(merr.KindCorrupt, "demux", "", fmt.Sprintf("rtp packet at offset %d", off), err)
		}
		pkts = append(pkts, p)
		off += n
	}
	return pkts, nil
}

// RTPConfig controls reordering and gap detection.
type RTPConfig struct {
	ClockRate     uint32 // e.g. 90000
	ReorderWindow int    // packets; late packets beyond the window are dropped
	GapThreshold  int    // missing-run length that forms an explicit hole
}

// RTPFrame is a de-jittered frame on the shared nanosecond timebase.
type RTPFrame struct {
	Seq          uint16
	ExtSeq       int64
	Timestamp    uint32
	ExtTimestamp int64
	TimeNanos    int64 // relative to the first emitted packet
	Payload      []byte
}

// RTPGap is an explicit hole in the sequence space.
type RTPGap struct {
	StartExtSeq int64
	Count       int
}

// RTPStats summarizes loss, duplication and reordering outcomes.
type RTPStats struct {
	Received           int
	Duplicates         int
	DroppedLate        int
	Lost               int
	TimestampAnomalies int
	Gaps               []RTPGap
}

// ProcessRTP de-jitters packets: duplicates are not emitted twice, packets
// arriving outside the reorder window are dropped and counted, sequence
// wrap is extended, and missing runs at or above the gap threshold become
// explicit holes that propagate to compositing quality stats.
func ProcessRTP(pkts []rtp.Packet, cfg RTPConfig) ([]RTPFrame, RTPStats, error) {
	if cfg.ReorderWindow < 1 {
		return nil, RTPStats{}, merr.Paramf("demux", "reorder window must be >= 1")
	}
	var stats RTPStats
	stats.Received = len(pkts)
	var seqExt timeline.SeqExtender
	var tsExt timeline.TsExtender
	type entry struct {
		extSeq int64
		extTs  int64
		pkt    rtp.Packet
	}
	buf := map[int64]entry{}
	seen := map[int64]bool{}
	var emitted []entry
	expected := int64(-1)

	drain := func(force bool) {
		for {
			if e, ok := buf[expected]; ok {
				emitted = append(emitted, e)
				delete(buf, expected)
				expected++
				continue
			}
			if len(buf) == 0 {
				return
			}
			hi := int64(-1)
			next := int64(-1)
			for k := range buf {
				if k > hi {
					hi = k
				}
				if k > expected && (next == -1 || k < next) {
					next = k
				}
			}
			if next == -1 {
				return
			}
			if !force && hi-expected+1 <= int64(cfg.ReorderWindow) {
				return // still inside the reorder window
			}
			n := next - expected
			stats.Lost += int(n)
			if int(n) >= cfg.GapThreshold {
				stats.Gaps = append(stats.Gaps, RTPGap{StartExtSeq: expected, Count: int(n)})
			}
			expected = next
		}
	}

	for _, p := range pkts {
		ext := int64(seqExt.Extend(p.SequenceNumber))
		ets := tsExt.Extend(p.Timestamp)
		if expected == -1 {
			expected = ext
		}
		switch {
		case seen[ext]:
			stats.Duplicates++
		case ext < expected:
			stats.DroppedLate++
		default:
			seen[ext] = true
			buf[ext] = entry{extSeq: ext, extTs: ets, pkt: p}
			drain(false)
		}
	}
	drain(true)

	// map extended timestamps onto the shared nanosecond timebase
	frames := make([]RTPFrame, 0, len(emitted))
	var baseTs int64
	var prevTime int64
	for i, e := range emitted {
		if i == 0 {
			baseTs = e.extTs
		}
		t := timeline.TicksToNanos(e.extTs-baseTs, int64(cfg.ClockRate))
		if i > 0 && t <= prevTime {
			stats.TimestampAnomalies++
			t = prevTime + 1 // clamp to keep frame times monotonic
		}
		prevTime = t
		frames = append(frames, RTPFrame{
			Seq:          e.pkt.SequenceNumber,
			ExtSeq:       e.extSeq,
			Timestamp:    e.pkt.Timestamp,
			ExtTimestamp: e.extTs,
			TimeNanos:    t,
			Payload:      e.pkt.Payload,
		})
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].ExtSeq < frames[j].ExtSeq })
	return frames, stats, nil
}
