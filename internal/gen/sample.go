// Package gen synthesizes deterministic H.264 RTP/RTCP sample streams with
// controllable loss, reordering, duplication, truncation and clock drift.
package gen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"mediadag/internal/ingest"
)

// Params controls sample generation.
type Params struct {
	Seed          int64
	DurationMs    int  // stream duration (default 6000)
	FPS           int  // frames per second (default 30)
	Kbps          int  // target bitrate
	IDREveryMs    int  // keyframe interval (default 2000)
	LossEvery     int  // drop every Nth media packet; 0 disables
	ReorderWindow int  // shuffle chunk size; 0 disables
	DupEvery      int  // duplicate every Nth media packet; 0 disables
	Truncate      bool // drop the last FU-A fragment of one IDR frame
	NoRTCP        bool // omit all sender reports (no clock anchor)
}

// Sample is a generated packet stream plus its input digest.
type Sample struct {
	Events []ingest.Event
	Digest string
}

const (
	mtuPayload = 1200
	seqStart   = 65530 // force 16-bit sequence wrap early in the stream
	tsStart    = 0xFFFF0000
	ssrc       = 0x16029
	ntpBaseSec = 1767225600 // 2026-01-01T00:00:00Z
	driftPPM   = 50.0
)

func (p *Params) defaults() {
	if p.DurationMs == 0 {
		p.DurationMs = 6000
	}
	if p.FPS == 0 {
		p.FPS = 30
	}
	if p.IDREveryMs == 0 {
		p.IDREveryMs = 2000
	}
}

type pkt struct {
	seq     uint16
	ts      uint32
	payload []byte
	marker  bool
	frame   int
}

// Generate builds the impaired event stream deterministically from p.
func Generate(p Params) *Sample {
	p.defaults()
	rng := rand.New(rand.NewSource(p.Seed))
	nFrames := p.DurationMs * p.FPS / 1000
	ticksPerFrame := uint32(90000 / p.FPS)
	avgBytes := p.Kbps * 1000 / 8 / p.FPS
	idrBytes := avgBytes * 5 / 2
	nonIDR := (avgBytes*p.IDREveryMs*p.FPS/1000 - idrBytes) / (p.IDREveryMs*p.FPS/1000 - 1)

	var pkts []pkt
	seq := uint16(seqStart)
	mkPayload := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}
	emitNAL := func(frame int, ts uint32, nalType byte, size int, marker bool) {
		if size < 1 {
			size = 1
		}
		body := mkPayload(size - 1)
		if size <= mtuPayload {
			pl := append([]byte{0x60 | nalType}, body...)
			pkts = append(pkts, pkt{seq, ts, pl, marker, frame})
			seq++
			return
		}
		// FU-A fragmentation
		off := 0
		for off < len(body) {
			n := mtuPayload - 2
			if n > len(body)-off {
				n = len(body) - off
			}
			hdr := nalType
			if off == 0 {
				hdr |= 0x80
			}
			last := off+n == len(body)
			if last {
				hdr |= 0x40
			}
			pl := append([]byte{0x60 | 28, hdr}, body[off:off+n]...)
			pkts = append(pkts, pkt{seq, ts, pl, last && marker, frame})
			seq++
			off += n
		}
	}

	srEveryFrames := p.IDREveryMs * p.FPS / 1000
	srBefore := map[int]rtcp.SenderReport{}
	if !p.NoRTCP {
		for f := 0; f < nFrames; f += srEveryFrames {
			el := float64(f) / float64(p.FPS)
			rtpTS := tsStart + uint32(f)*ticksPerFrame
			rtpTS += uint32(el * 90000 * driftPPM / 1e6) // sender clock drift
			sec := uint32(ntpBaseSec) + uint32(el)
			frac := uint32((el - float64(uint32(el))) * (1 << 32))
			srBefore[f] = rtcp.SenderReport{
				SSRC:    ssrc,
				NTPTime: uint64(sec)<<32 | uint64(frac),
				RTPTime: rtpTS,
			}
		}
	}

	for f := 0; f < nFrames; f++ {
		ts := tsStart + uint32(f)*ticksPerFrame
		idr := f%srEveryFrames == 0
		if idr {
			emitNAL(f, ts, 7, 16, false) // SPS
			emitNAL(f, ts, 8, 4, false)  // PPS
			emitNAL(f, ts, 5, idrBytes, true)
		} else {
			emitNAL(f, ts, 1, nonIDR, true)
		}
	}

	// impairment: truncation of one IDR frame's final fragment
	if p.Truncate {
		victim := nFrames * 2 / 3 / srEveryFrames * srEveryFrames // an IDR frame
		for i := len(pkts) - 1; i >= 0; i-- {
			if pkts[i].frame == victim && len(pkts[i].payload) > 1 &&
				pkts[i].payload[0]&0x1F == 28 && pkts[i].payload[1]&0x40 != 0 {
				pkts = append(pkts[:i], pkts[i+1:]...)
				break
			}
		}
	}

	// impairment: loss
	if p.LossEvery > 0 {
		kept := pkts[:0]
		for i, q := range pkts {
			if (i+1)%p.LossEvery != 0 {
				kept = append(kept, q)
			}
		}
		pkts = kept
	}

	// impairment: duplicates
	if p.DupEvery > 0 {
		var out []pkt
		for i, q := range pkts {
			out = append(out, q)
			if (i+1)%p.DupEvery == 0 {
				out = append(out, q)
			}
		}
		pkts = out
	}

	// impairment: reorder within chunks of ReorderWindow
	if p.ReorderWindow > 1 {
		for lo := 0; lo < len(pkts); lo += p.ReorderWindow {
			hi := lo + p.ReorderWindow
			if hi > len(pkts) {
				hi = len(pkts)
			}
			rng.Shuffle(hi-lo, func(i, j int) {
				pkts[lo+i], pkts[lo+j] = pkts[lo+j], pkts[lo+i]
			})
		}
	}

	// merge: SR events before the first surviving packet of their frame
	firstPktOfFrame := map[int]int{}
	for i, q := range pkts {
		if _, ok := firstPktOfFrame[q.frame]; !ok {
			firstPktOfFrame[q.frame] = i
		}
	}
	srAtPkt := map[int]rtcp.SenderReport{}
	for f, sr := range srBefore {
		if idx, ok := firstPktOfFrame[f]; ok {
			srAtPkt[idx] = sr
		}
	}

	s := &Sample{}
	h := sha256.New()
	var arrival int64
	addDigest := func(q pkt) {
		var b [8]byte
		binary.BigEndian.PutUint16(b[0:2], q.seq)
		binary.BigEndian.PutUint32(b[2:6], q.ts)
		h.Write(b[:6])
		h.Write(q.payload)
	}
	for i, q := range pkts {
		if sr, ok := srAtPkt[i]; ok {
			s.Events = append(s.Events, ingest.Event{Kind: ingest.KindRTCP, Arrival: arrival, SR: &sr})
			arrival++
			var b [12]byte
			binary.BigEndian.PutUint64(b[0:8], sr.NTPTime)
			binary.BigEndian.PutUint32(b[8:12], sr.RTPTime)
			h.Write(b[:])
		}
		s.Events = append(s.Events, ingest.Event{
			Kind:    ingest.KindRTP,
			Arrival: arrival,
			Packet: &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         q.marker,
					PayloadType:    96,
					SequenceNumber: q.seq,
					Timestamp:      q.ts,
					SSRC:           ssrc,
				},
				Payload: q.payload,
			},
		})
		arrival++
		addDigest(q)
	}
	s.Digest = hex.EncodeToString(h.Sum(nil))
	return s
}
