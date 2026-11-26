package ingest

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/pion/rtp"

	"mediasync/internal/media"
)

const DefaultReorderWindow = 4

type OpusPacket struct {
	Ordinal     int    `json:"ordinal"`
	ByteOffset  int64  `json:"byte_offset"`
	Sequence    uint16 `json:"sequence"`
	ExtSequence int64  `json:"extended_sequence"`
	Timestamp   uint32 `json:"timestamp"`
	StartTicks  int64  `json:"start_ticks"`
	Payload     []byte `json:"-"`
}

type RTPStats struct {
	Datagrams    int     `json:"datagrams"`
	Unique       int     `json:"unique"`
	Duplicates   int     `json:"duplicates"`
	Gaps         int     `json:"gaps"`
	LossFraction float64 `json:"loss_fraction"`
	GapSequences []int64 `json:"gap_sequences"`
}

type OpusResult struct {
	TrackID string       `json:"track_id"`
	Packets []OpusPacket `json:"packets"`
	Stats   RTPStats     `json:"stats"`
}

// ReadOpusTrack reads a length-prefixed datagram container. Each record is a
// complete RTP packet. The two-byte prefix gives durable byte offsets for
// malformed-input diagnostics without changing RTP parsing itself.
func ReadOpusTrack(spec TrackSpec, inputDir string) (*OpusResult, error) {
	path, err := TrackPath(inputDir, spec.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.InputError("ingest.rtp", spec.ID, err)
	}
	window := spec.ReorderWindow
	if window == 0 {
		window = DefaultReorderWindow
	}
	packets, stats, err := ParseRTPContainer(data, spec.ID, spec.OriginTicks, window)
	if err != nil {
		return nil, err
	}
	stats.Datagrams = len(packets) + stats.Duplicates
	unique := len(packets)
	stats.Unique = unique
	denom := unique + stats.Gaps
	if denom > 0 {
		stats.LossFraction = float64(stats.Gaps) / float64(denom)
	}
	return &OpusResult{TrackID: spec.ID, Packets: packets, Stats: stats}, nil
}

func ParseRTPContainer(data []byte, trackID string, originTicks int64, window int) ([]OpusPacket, RTPStats, error) {
	if window <= 0 {
		window = DefaultReorderWindow
	}
	q := newReorderQueue(window)
	accepted := make([]OpusPacket, 0)
	ordinal := 0

	for pos := 0; pos < len(data); ordinal++ {
		recordStart := int64(pos)
		if len(data)-pos < 2 {
			return nil, RTPStats{}, rtpFault(trackID, ordinal, recordStart,
				fmt.Errorf("truncated length prefix at end of file"))
		}
		length := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		packetStart := int64(pos + 2)
		pos += 2
		if length < 12 {
			return nil, RTPStats{}, rtpFault(trackID, ordinal, packetStart,
				fmt.Errorf("RTP record length %d is shorter than 12 bytes", length))
		}
		if pos+length > len(data) {
			return nil, RTPStats{}, rtpFault(trackID, ordinal, packetStart,
				fmt.Errorf("truncated RTP record: need %d bytes, have %d", length, len(data)-pos))
		}
		raw := data[pos : pos+length]
		pos += length

		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, RTPStats{}, rtpFault(trackID, ordinal, packetStart, err)
		}
		if pkt.Version != 2 {
			return nil, RTPStats{}, rtpFault(trackID, ordinal, packetStart,
				fmt.Errorf("unsupported RTP version %d", pkt.Version))
		}

		ext := q.extend(pkt.SequenceNumber)
		if q.accept(ext) {
			continue
		}
		tsTicks, err := media.FromOpusSamples(pkt.Timestamp)
		if err != nil {
			fe := media.OverflowError("ingest.rtp", trackID, err).
				WithPacket(ordinal).WithOffset(packetStart)
			return nil, RTPStats{}, fe
		}
		start, err := media.CheckedAdd(originTicks, tsTicks)
		if err != nil {
			fe := media.OverflowError("ingest.rtp", trackID, err).
				WithPacket(ordinal).WithOffset(packetStart)
			return nil, RTPStats{}, fe
		}
		accepted = append(accepted, OpusPacket{
			Ordinal: ordinal, ByteOffset: packetStart,
			Sequence: pkt.SequenceNumber, ExtSequence: ext,
			Timestamp: pkt.Timestamp, StartTicks: start, Payload: append([]byte(nil), pkt.Payload...),
		})
	}

	q.closeRemaining()
	sort.Slice(accepted, func(i, j int) bool { return accepted[i].ExtSequence < accepted[j].ExtSequence })
	stats := RTPStats{
		Duplicates:   q.duplicates,
		Gaps:         q.gaps,
		GapSequences: append([]int64(nil), q.missing...),
	}
	return accepted, stats, nil
}

func rtpFault(track string, ordinal int, offset int64, err error) *media.Error {
	return media.InputError("ingest.rtp", track, err).WithPacket(ordinal).WithOffset(offset)
}

// ExtendSequence maps a uint16 RTP sequence number into a monotonic int64.
// prevMax is the largest extended sequence already observed; primed reports
// whether it is valid.
func ExtendSequence(prevMax int64, primed bool, seq uint16) int64 {
	s := int64(seq)
	if !primed {
		return s
	}
	cycle := prevMax / 65536
	candidate := cycle*65536 + s
	if candidate-prevMax > 32768 {
		candidate -= 65536
	} else if prevMax-candidate > 32768 {
		candidate += 65536
	}
	return candidate
}

type reorderQueue struct {
	window     int
	primed     bool
	next       int64
	maxSeen    int64
	seen       map[int64]bool
	duplicates int
	gaps       int
	missing    []int64
}

func newReorderQueue(window int) *reorderQueue {
	if window <= 0 {
		window = DefaultReorderWindow
	}
	return &reorderQueue{window: window, seen: make(map[int64]bool)}
}

func (q *reorderQueue) extend(seq uint16) int64 {
	return ExtendSequence(q.maxSeen, q.primed, seq)
}

// accept records one extended sequence. It returns true for duplicates.
// Gaps are finalized only after window newer sequence numbers establish that
// the missing packet is unlikely to arrive out of order.
func (q *reorderQueue) accept(ext int64) bool {
	if q.seen[ext] {
		q.duplicates++
		return true
	}
	q.seen[ext] = true
	if !q.primed {
		q.primed = true
		q.next = ext
		q.maxSeen = ext
	}
	if ext > q.maxSeen {
		q.maxSeen = ext
	}
	for q.next <= q.maxSeen {
		if q.seen[q.next] {
			q.next++
			continue
		}
		if q.maxSeen-q.next >= int64(q.window) {
			q.gaps++
			q.missing = append(q.missing, q.next)
			q.next++
			continue
		}
		break
	}
	return false
}

func (q *reorderQueue) closeRemaining() {
	if !q.primed {
		return
	}
	for q.next <= q.maxSeen {
		if !q.seen[q.next] {
			q.gaps++
			q.missing = append(q.missing, q.next)
		}
		q.next++
	}
}
