package demux

import (
	"encoding/binary"
	"os"
	"sort"

	"github.com/pion/rtp/v2"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

type RTPSourceKey struct {
	SSRC        uint32 `json:"ssrc"`
	PayloadType uint8  `json:"payload_type"`
}

type RTPGap struct {
	AfterSequence uint16 `json:"after_sequence"`
	FromSequence  uint16 `json:"from_sequence"`
	ToSequence    uint16 `json:"to_sequence"`
	MissingCount  int    `json:"missing_count"`
	StartTS       int64  `json:"start_ts"`
	EndTS         int64  `json:"end_ts"`
	Offset        int64  `json:"offset"`
}

type RTPStream struct {
	Key              RTPSourceKey   `json:"key"`
	ClockRate        uint32         `json:"clock_rate"`
	Timebase         clock.Timebase `json:"timebase"`
	Samples          []*Sample      `json:"samples"`
	Gaps             []RTPGap       `json:"gaps,omitempty"`
	DuplicatePackets int            `json:"duplicate_packets"`
	ReceivedPackets  int            `json:"received_packets"`
	Issues           []*fault.Error `json:"issues,omitempty"`
}

type RTPDocument struct {
	Path    string       `json:"path"`
	Format  string       `json:"format"`
	Streams []*RTPStream `json:"streams"`
}

// DemuxRTP reads the local length-prefixed RTP packet container. Standard
// RTP headers are parsed by pion/rtp; sequence duplicates are counted but do
// not create samples. Missing sequence numbers become explicit gaps.
func DemuxRTP(path string, clockRate uint32) (*RTPDocument, error) {
	if clockRate == 0 {
		clockRate = 48000
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	doc := &RTPDocument{Path: path, Format: "rtp"}
	byKey := map[RTPSourceKey]*RTPStream{}
	type raw struct {
		pkt    rtp.Packet
		offset int64
	}
	rawByKey := map[RTPSourceKey][]raw{}

	var offset int64
	for len(data) > 0 {
		frameStart := offset
		if len(data) < 4 {
			return doc, fault.At(fault.KindRTP, path, frameStart, "short RTP frame length prefix")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		offset += 4
		data = data[4:]
		if n < 12 || n > len(data) {
			fe := fault.At(fault.KindRTP, path, frameStart, "RTP frame length %d exceeds %d bytes", n, len(data))
			fe.Recoverable = true
			return doc, fe
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[:n]); err != nil {
			fe := fault.Track(fault.KindRTP, path, 0, frameStart, err, true)
			doc.addIssueAll(fe)
		} else {
			key := RTPSourceKey{SSRC: pkt.SSRC, PayloadType: pkt.PayloadType}
			st := byKey[key]
			if st == nil {
				st = &RTPStream{Key: key, ClockRate: clockRate, Timebase: clock.TickRate(clockRate)}
				byKey[key] = st
				doc.Streams = append(doc.Streams, st)
			}
			if ext := pkt.GetExtension(1); len(ext) == 8 {
				// Optional high bits extension used by the local wraparound
				// fixture: big-endian uint32 cycle count followed by reserved.
				_ = binary.BigEndian.Uint32(ext[:4])
			}
			rawByKey[key] = append(rawByKey[key], raw{pkt: pkt, offset: frameStart})
		}
		offset += int64(n)
		data = data[n:]
	}

	for key, raws := range rawByKey {
		st := byKey[key]
		seen := map[uint64]bool{}
		var uniq []rtpUnique
		var lastSeq *uint64
		var lastTS int64
		for _, r := range raws {
			st.ReceivedPackets++
			seq := UnwrapSequence(lastSeq, r.pkt.SequenceNumber)
			lastSeq = &seq
			if seen[seq] {
				st.DuplicatePackets++
				continue
			}
			seen[seq] = true
			ts := UnwrapTimestamp(lastTS, r.pkt.Timestamp)
			if len(uniq) > 0 {
				lastTS = ts
			} else {
				lastTS = ts
			}
			uniq = append(uniq, rtpUnique{seq: seq, ts: ts, pkt: r.pkt, offset: r.offset})
		}
		sortUnique(uniq)
		var prevSeq uint64
		var havePrev bool
		for i, u := range uniq {
			if havePrev && u.seq > prevSeq+1 {
				missing := int(u.seq-prevSeq) - 1
				st.Gaps = append(st.Gaps, RTPGap{
					AfterSequence: uint16(prevSeq), FromSequence: uint16(prevSeq + 1),
					ToSequence: uint16(u.seq - 1), MissingCount: missing,
					StartTS: st.Samples[len(st.Samples)-1].DTS, EndTS: u.ts, Offset: u.offset,
				})
			}
			havePrev = true
			prevSeq = u.seq
			duration := inferDuration(uniq, i, clockRate)
			s := &Sample{
				Index: i, TrackID: key.SSRC, DTS: u.ts, PTS: u.ts, Duration: duration,
				Offset: u.offset, Size: len(u.pkt.Payload), Hash: HashPayload(u.pkt.Payload),
				Payload: append([]byte(nil), u.pkt.Payload...), Flags: rtpFlags(u.pkt),
			}
			st.Samples = append(st.Samples, s)
		}
	}
	return doc, nil
}

// UnwrapTimestamp expands a wrapping 32-bit RTP timestamp using signed modular
// difference. prev is the expanded previous timestamp.
func UnwrapTimestamp(prev int64, cur uint32) int64 {
	if prev == 0 {
		return int64(cur)
	}
	delta := int64(int32(uint32(cur) - uint32(prev)))
	return prev + delta
}

func UnwrapSequence(prev *uint64, cur uint16) uint64 {
	if prev == nil {
		return uint64(cur)
	}
	low := uint16(*prev)
	d := int32(cur) - int32(low)
	if d > 32768 {
		d -= 65536
	} else if d < -32768 {
		d += 65536
	}
	return *prev + uint64(d)
}

type rtpUnique struct {
	seq    uint64
	ts     int64
	pkt    rtp.Packet
	offset int64
}

func sortUnique(u []rtpUnique) {
	sort.SliceStable(u, func(i, j int) bool { return u[i].seq < u[j].seq })
}

func inferDuration(u []rtpUnique, i int, rate uint32) int64 {
	if i+1 < len(u) && u[i+1].ts > u[i].ts {
		return u[i+1].ts - u[i].ts
	}
	return int64(rate / 50) // 20 ms fallback
}

func rtpFlags(p rtp.Packet) uint32 {
	var f uint32
	if p.Marker {
		f |= 1
	}
	return f
}

func (d *RTPDocument) addIssueAll(fe *fault.Error) {
	for _, s := range d.Streams {
		s.Issues = append(s.Issues, fe)
	}
}
