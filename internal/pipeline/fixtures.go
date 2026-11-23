package pipeline

import (
	"encoding/binary"
	"math/rand"
	"sort"

	"github.com/pion/rtp"

	"mediad/internal/clock"
	rtpkit "mediad/internal/rtp"
)

// Chunk is one arrival of framed bytes on the reference clock.
type Chunk struct {
	ArrivalMs int64
	Data      []byte
	order     int
}

// Truth records ground truth the generator knows and the pipeline must
// estimate: the real A/V offset and the number of media packets lost.
type Truth struct {
	TrueOffsetMs int64
	SentMedia    int
	LostMedia    int
}

// Spec describes one generated scenario.
type Spec struct {
	Name             string
	Loss             float64
	ReorderMs        int64
	FeedbackDelayMs  int64
	SendSR           bool
	WrapSeq          bool
	TruncateTail     bool
	CorruptExtension bool
	CorruptFraming   bool
	Duplicates       float64
	InjectAmbiguous  bool
}

const (
	durationMs   = 3000
	videoStepMs  = 33
	audioStepMs  = 20
	baseDelayMs  = 40
	videoSSRC    = 0x11111111
	audioSSRC    = 0x22222222
	trueOffsetMs = 25 // video presentation runs 25ms ahead of audio
)

// Fixtures returns the six declared acceptance scenarios.
func Fixtures() []Spec {
	base := Spec{
		Name: "normal", Loss: 0.05, ReorderMs: 80, FeedbackDelayMs: 30,
		SendSR: true, Duplicates: 0.01,
	}
	wrap := base
	wrap.Name, wrap.WrapSeq, wrap.InjectAmbiguous = "wrap", true, true
	trunc := base
	trunc.Name, trunc.TruncateTail = "truncated", true
	corrupt := base
	corrupt.Name, corrupt.CorruptExtension, corrupt.CorruptFraming = "corrupt", true, true
	late := base
	late.Name, late.FeedbackDelayMs = "late-rtcp", 220
	nosr := base
	nosr.Name, nosr.SendSR = "no-sr", false
	return []Spec{base, wrap, trunc, corrupt, late, nosr}
}

type pendingPacket struct {
	sendMs int64
	frame  []byte
	media  bool // media packet (subject to loss)
}

// Generate deterministically builds the framed byte stream for spec.
func Generate(spec Spec, seed int64) ([]Chunk, Truth) {
	rng := rand.New(rand.NewSource(seed))
	var truth Truth
	truth.TrueOffsetMs = trueOffsetMs
	var pkts []pendingPacket

	vseq := uint16(5000)
	if spec.WrapSeq {
		vseq = 65470
	}
	var aseq uint16 = 20000
	videoTSBase := uint32(clock.RTPClockVideo.MillisToTicks(trueOffsetMs))

	// Video: 90 kHz, keyframe every 45 frames.
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
		pkts = append(pkts, mkPacket(vseq, ts, videoSSRC, rtpkit.PTVideo, key, payload, ms))
		vseq++
		truth.SentMedia++
	}
	// Audio: 48 kHz, 20 ms frames.
	for ms := int64(0); ms < durationMs; ms += audioStepMs {
		payload := make([]byte, 160)
		rng.Read(payload)
		ts := uint32(clock.RTPClockAudio.MillisToTicks(ms))
		pkts = append(pkts, mkPacket(aseq, ts, audioSSRC, rtpkit.PTAudio, false, payload, ms))
		aseq++
		truth.SentMedia++
	}
	// Sender reports every second, honestly mapping RTP to wall clock.
	if spec.SendSR {
		for ms := int64(500); ms < durationMs; ms += 1000 {
			vts := videoTSBase + uint32(clock.RTPClockVideo.MillisToTicks(ms))
			pkts = append(pkts, pendingPacket{ms, mkSR(videoSSRC, ms, vts), false})
			ats := uint32(clock.RTPClockAudio.MillisToTicks(ms))
			pkts = append(pkts, pendingPacket{ms, mkSR(audioSSRC, ms, ats), false})
		}
	}
	// Ambiguous sequence injection: a stray packet from a far-off cycle.
	if spec.InjectAmbiguous {
		payload := []byte{0x00}
		p := mkPacket(vseq-2000, videoTSBase, videoSSRC, rtpkit.PTVideo, false, payload, 1500)
		pkts = append(pkts, p)
		truth.SentMedia++
	}

	// Network: loss, jitter, reorder, duplication.
	type arrival struct {
		at    int64
		frame []byte
	}
	var arr []arrival
	for _, p := range pkts {
		if p.media && rng.Float64() < spec.Loss {
			truth.LostMedia++
			continue
		}
		at := p.sendMs + baseDelayMs + int64(rng.Intn(20))
		if spec.ReorderMs > 0 && rng.Float64() < 0.3 {
			at += int64(rng.Intn(int(spec.ReorderMs)))
		}
		arr = append(arr, arrival{at, p.frame})
		if p.media && spec.Duplicates > 0 && rng.Float64() < spec.Duplicates {
			arr = append(arr, arrival{at + 3, append([]byte(nil), p.frame...)})
		}
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].at < arr[j].at })

	var chunks []Chunk
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
		chunks = append(chunks, Chunk{ArrivalMs: a.at, Data: data, order: i})
	}
	// Truncated tail: cut the final chunk mid-packet.
	if spec.TruncateTail && len(chunks) > 0 {
		last := &chunks[len(chunks)-1]
		if len(last.Data) > 8 {
			last.Data = last.Data[:len(last.Data)/2]
		}
	}
	return chunks, truth
}

func mkPacket(seq uint16, ts, ssrc uint32, pt uint8, marker bool, payload []byte, sendMs int64) pendingPacket {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: marker, PayloadType: pt,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	frame := append([]byte{byte(len(raw) >> 8), byte(len(raw))}, raw...)
	return pendingPacket{sendMs, frame, true}
}

// mkSR builds a 28-byte RTCP Sender Report mapping wallMs to rtpTs.
func mkSR(ssrc uint32, wallMs int64, rtpTs uint32) []byte {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	binary.BigEndian.PutUint32(b[4:], ssrc)
	sec := uint64(wallMs)/1000 + 2208988800
	frac := uint64(wallMs%1000) * (1 << 32) / 1000
	binary.BigEndian.PutUint32(b[8:], uint32(sec))
	binary.BigEndian.PutUint32(b[12:], uint32(frac))
	binary.BigEndian.PutUint32(b[16:], rtpTs)
	frame := append([]byte{0x00, 28}, b...)
	return frame
}

// corruptExtension sets the X bit and plants an extension header whose
// declared length exceeds the packet, inside the frame's length prefix.
func corruptExtension(frame []byte) []byte {
	body := append([]byte(nil), frame[2:]...)
	if len(body) < 12 {
		return frame
	}
	body[0] |= 0x10 // X bit
	ext := []byte{0x00, 0x00, 0xFF, 0xFF} // profile + absurd length
	out := append(body[:12:12], ext...)
	out = append(out, body[12:]...)
	return append([]byte{byte(len(out) >> 8), byte(len(out))}, out...)
}
