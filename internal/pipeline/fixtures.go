package pipeline

import (
	"encoding/binary"
	"math/rand"
	"sort"

	"mediad/internal/clock"
	"mediad/internal/model"
	"mediad/internal/recovery"
	rtpkit "mediad/internal/rtp"
)

type pendingPacket struct {
	sendMs int64
	frame  []byte
	ev     model.Event
	media  bool // media packet (subject to loss)
	lost   bool
}

// mediaEvent fills the flow's identifiers into the sender's event record.
func (p *pendingPacket) mediaEvent(media model.MediaType, rate clock.Rate) {
	p.ev.Kind = model.KindPacket
	p.ev.Media = media
	p.ev.EventMs = rate.ToMillis(uint64(p.ev.RTPTs))
	p.ev.ArrivalMs = p.sendMs
	p.ev.Size = len(p.frame)
}

// Generate deterministically builds the framed byte stream for spec.
func Generate(spec Spec, seed int64) ([]Chunk, Truth) {
	rng := rand.New(rand.NewSource(seed))
	var truth Truth
	truth.TrueOffsetMs = trueOffsetMs
	var pkts []pendingPacket
	var lostPkts []pendingPacket

	vseq := uint16(5000)
	if spec.WrapSeq {
		vseq = 65470
	}
	var aseq uint16 = 20000
	videoTSBase := uint32(clock.RTPClockVideo.MillisToTicks(trueOffsetMs))

	for i, ms := int64(0), int64(0); ms < durationMs; i, ms = i+1, ms+videoStepMs {
		key := i%45 == 0
		payload := make([]byte, 600+rng.Intn(800))
		rng.Read(payload)
		if key {
			payload[0] |= 0x01
		} else {
			payload[0] &^= 0x01
		}
		ts := videoTSBase + uint32(clock.RTPClockVideo.MillisToTicks(ms))
		p := mkPacket(vseq, ts, videoSSRC, rtpkit.PTVideo, key, payload, ms)
		p.mediaEvent(model.MediaVideo, clock.RTPClockVideo)
		pkts = append(pkts, p)
		vseq++
		truth.SentMedia++
	}
	for ms := int64(0); ms < durationMs; ms += audioStepMs {
		payload := make([]byte, 160)
		rng.Read(payload)
		ts := uint32(clock.RTPClockAudio.MillisToTicks(ms))
		p := mkPacket(aseq, ts, audioSSRC, rtpkit.PTAudio, false, payload, ms)
		p.mediaEvent(model.MediaAudio, clock.RTPClockAudio)
		pkts = append(pkts, p)
		aseq++
		truth.SentMedia++
	}
	if spec.SendSR {
		for ms := int64(500); ms < durationMs; ms += 1000 {
			// The reports map a media-time RTP clock onto honest wall
			// time. Video packets carry an extra timestamp base, which
			// makes their presentation run trueOffsetMs ahead of audio;
			// the SR path must recover exactly that offset.
			drift := ms * spec.DriftPPM / 1_000_000
			vts := uint32(clock.RTPClockVideo.MillisToTicks(ms))
			pkts = append(pkts, pendingPacket{sendMs: ms, frame: mkSR(videoSSRC, ms+drift, vts)})
			ats := uint32(clock.RTPClockAudio.MillisToTicks(ms))
			pkts = append(pkts, pendingPacket{sendMs: ms, frame: mkSR(audioSSRC, ms+drift, ats)})
		}
	}
	// Ambiguous sequence injection: a stray packet from a far-off cycle.
	if spec.InjectAmbiguous {
		payload := []byte{0x00}
		p := mkPacket(vseq-2000, videoTSBase, videoSSRC, rtpkit.PTVideo, false, payload, 1500)
		p.mediaEvent(model.MediaVideo, clock.RTPClockVideo)
		pkts = append(pkts, p)
		truth.SentMedia++
	}

	// Sender log and network loss are decided in transmission order so the
	// seed fully determines both. Only media packets enter the retransmit
	// cache; RTCP is never retransmitted.
	for i := range pkts {
		p := &pkts[i]
		if !p.media {
			continue
		}
		truth.Sent = append(truth.Sent, Send{AtMs: p.sendMs, Event: p.ev})
		if rng.Float64() < spec.Loss {
			p.lost = true
			truth.LostMedia++
			truth.LostKeys = append(truth.LostKeys, recovery.Key{SSRC: p.ev.SSRC, Seq16: p.ev.Seq16})
			lostPkts = append(lostPkts, *p)
		}
	}

	// Feedback: NACK every lost packet, plus controlled fault injections.
	fb := make([]Chunk, 0, len(lostPkts))
	for i, p := range lostPkts {
		delay := spec.FeedbackDelayMs
		if spec.LateNACKMs > 0 && spec.LateNACKEvery > 0 && i%spec.LateNACKEvery == 0 {
			delay = spec.LateNACKMs
		}
		fb = append(fb, Chunk{ArrivalMs: p.sendMs + delay, Data: mkNACK(p.ev.SSRC, p.ev.Seq16)})
		if spec.DupNACKEvery > 0 && i%spec.DupNACKEvery == 0 {
			fb = append(fb, Chunk{ArrivalMs: p.sendMs + delay + 6, Data: mkNACK(p.ev.SSRC, p.ev.Seq16)})
		}
	}
	if spec.PhantomNACK {
		// Feedback for a packet this sender never transmitted: the
		// receiver's lookup resolves but the cache cannot.
		pid := uint16(vseq + 1000)
		fb = append(fb, Chunk{ArrivalMs: 1200, Data: mkNACK(videoSSRC, pid)})
	}
	if spec.SendSR {
		for ms := int64(700); ms < durationMs; ms += 1000 {
			fb = append(fb, Chunk{ArrivalMs: ms + baseDelayMs, Data: mkRR(videoSSRC)})
			fb = append(fb, Chunk{ArrivalMs: ms + baseDelayMs, Data: mkRR(audioSSRC)})
		}
	}

	// Network: jitter, reorder, duplication for everything not lost.
	// Arrival times are recorded alongside the packet so the late-sample
	// injection can act on the final, post-sort schedule.
	type arrival struct {
		at    int64
		frame []byte
		media bool
	}
	var arr []arrival
	for _, p := range pkts {
		if p.media && p.lost {
			continue
		}
		// RTCP is not subject to the media jitter/reorder model: keeping
		// its one-way delay fixed leaves the NTP-versus-arrival
		// comparison sensitive to clock drift instead of to network
		// noise, while media packets exercise jitter separately.
		at := p.sendMs + baseDelayMs
		if p.media {
			at += int64(rng.Intn(20))
			if spec.ReorderMs > 0 && rng.Float64() < 0.3 {
				at += int64(rng.Intn(int(spec.ReorderMs)))
			}
		}
		arr = append(arr, arrival{at, p.frame, p.media})
		if p.media && spec.Duplicates > 0 && rng.Float64() < spec.Duplicates {
			arr = append(arr, arrival{at + 3, append([]byte(nil), p.frame...), false})
		}
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].at < arr[j].at })
	if spec.LateSampleMs > 0 && spec.LateSampleEvery > 0 {
		k := 0
		for i := range arr {
			if !arr[i].media {
				continue
			}
			if k%spec.LateSampleEvery == 0 {
				arr[i].at += spec.LateSampleMs
			}
			k++
		}
		sort.SliceStable(arr, func(i, j int) bool { return arr[i].at < arr[j].at })
	}

	chunks := make([]Chunk, 0, len(arr)+len(fb))
	for i, a := range arr {
		data := a.frame
		// Corrupt one video packet's extension header mid-stream.
		if spec.CorruptExtension && i == len(arr)/3 {
			data = corruptExtension(a.frame)
		}
		// Corrupt framing once: implausible length byte ahead of data.
		if spec.CorruptFraming && i == len(arr)/2 {
			data = append([]byte{0x00, 0x01, 0xFF}, a.frame...)
		}
		chunks = append(chunks, Chunk{ArrivalMs: a.at, Data: data, order: len(chunks)})
	}
	chunks = append(chunks, fb...)
	if spec.TruncateTail {
		chunks = truncateTail(chunks)
	}
	return chunks, truth
}

// truncateTail makes the byte stream end inside a media frame. The tail
// is the media packet with the latest arrival: it is cut in half and any
// chunk that would arrive later is dropped, so no trailing RTCP frame can
// let the parser read the partial frame as complete. Feedback arriving
// earlier in the stream is retained.
func truncateTail(chunks []Chunk) []Chunk {
	last := -1
	for i, ch := range chunks {
		if !isMediaFrame(ch.Data) {
			continue
		}
		if last < 0 || ch.ArrivalMs > chunks[last].ArrivalMs {
			last = i
		}
	}
	if last < 0 {
		return chunks
	}
	tail := chunks[last]
	cut := len(tail.Data) * 2 / 3
	if cut < 8 {
		cut = len(tail.Data) / 2
	}
	out := make([]Chunk, 0, last+1)
	for i, ch := range chunks {
		if i == last {
			continue
		}
		if ch.ArrivalMs < tail.ArrivalMs {
			out = append(out, ch)
		}
	}
	out = append(out, Chunk{ArrivalMs: tail.ArrivalMs, Data: tail.Data[:cut], order: tail.order})
	return out
}

// isMediaFrame reports whether data is a length-framed RTP packet with a
// declared media payload type (PT 96/97).
func isMediaFrame(data []byte) bool {
	if len(data) < 14 {
		return false
	}
	n := int(binary.BigEndian.Uint16(data))
	if n != len(data)-2 {
		return false
	}
	pt := data[3] & 0x7F
	return data[2]>>6 == 2 && (pt == 96 || pt == 97)
}
