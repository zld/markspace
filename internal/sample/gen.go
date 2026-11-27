package sample

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"
)

// RTP SSRC and clock used by the generated streams.
const (
	VideoSSRC   = 0x01020304
	VideoClock  = VideoTimescale
	rtpStartSeq = 1000
	rtpStartTS  = 50000
)

// RTPStream describes a generated RTP record stream.
type RTPStream struct {
	Records []byte `json:"-"` // ingest-format record stream
	Packets int    `json:"packets"`
	Unique  int    `json:"unique"`
	Lost    int    `json:"lost"`
	Dups    int    `json:"dups"`
}

// GenRTPStream builds a deterministic RTP packet stream of n media
// packets. With lossy=true it drops a deterministic subset, duplicates
// others, and reorders within a small window. Lost reports how many
// sequence numbers are missing; Unique how many distinct packets the
// stream carries.
func GenRTPStream(seed int64, n int, lossy bool) RTPStream {
	rng := rand.New(rand.NewSource(seed))
	type pkt struct{ seq int }
	var seqs []int
	lost, dups := 0, 0
	for i := 0; i < n; i++ {
		if lossy && i > 0 && i < n-1 && rng.Intn(10) == 0 {
			lost++
			continue // dropped: gap in the sequence
		}
		seqs = append(seqs, i)
		if lossy && rng.Intn(8) == 0 {
			seqs = append(seqs, i) // duplicate
			dups++
		}
	}
	if lossy {
		// Reorder within a sliding window of 3 (deterministic shuffle).
		for i := 0; i+2 < len(seqs); i += 3 {
			if rng.Intn(2) == 0 {
				seqs[i+1], seqs[i+2] = seqs[i+2], seqs[i+1]
			}
		}
	}
	var records []byte
	for _, s := range seqs {
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: uint16(rtpStartSeq + s),
				Timestamp:      uint32(rtpStartTS + s*VideoFrameDuration),
				SSRC:           VideoSSRC,
			},
			Payload: []byte{byte(s), byte(s >> 8), 0xAB},
		}
		raw, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(raw)))
		records = append(records, hdr[:]...)
		records = append(records, raw...)
	}
	return RTPStream{
		Records: records,
		Packets: len(seqs),
		Unique:  n - lost,
		Lost:    lost,
		Dups:    dups,
	}
}

// RTCPAnchor is one sender-report anchor, encoded as a record:
// 4 bytes SSRC, 8 bytes NTP nanoseconds, 4 bytes RTP timestamp.
type RTCPAnchor struct {
	SSRC     uint32
	NTPNanos int64
	RTPTS    uint32
}

// GenRTCPRecords builds the RTCP anchor record stream for the video track.
func GenRTCPRecords() []byte {
	a := RTCPAnchor{SSRC: VideoSSRC, NTPNanos: 1_000_000_000, RTPTS: rtpStartTS}
	var rec [16]byte
	binary.BigEndian.PutUint32(rec[0:4], a.SSRC)
	binary.BigEndian.PutUint64(rec[4:12], uint64(a.NTPNanos))
	binary.BigEndian.PutUint32(rec[12:16], a.RTPTS)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 16)
	return append(hdr[:], rec[:]...)
}

// VideoEvents returns the RTP timestamps of one event per frame, used
// for event→media-time alignment checks.
func VideoEvents(n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = uint32(rtpStartTS + i*VideoFrameDuration)
	}
	return out
}

// ExpectedMediaTimeNanos is the unified-timeline position of RTP
// timestamp ts given the generated anchor.
func ExpectedMediaTimeNanos(ts uint32) int64 {
	return 1_000_000_000 + int64(ts-rtpStartTS)*1_000_000_000/VideoTimescale
}
