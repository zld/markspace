// Package fixture generates self-contained, deterministic sample assets for
// the offline pipeline: line-oriented MXF headers, minimal ISO BMFF MP4
// bytes, a lossy RTP capture (duplicates, reordering, burst loss, 16-bit
// sequence wrap) and a varying-bandwidth trace. No external files are used;
// every byte is reproducible from the seed.
package fixture

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	rtppkg "mediasync/internal/rtp"
)

// Fault names the failure injected into a demo run.
type Fault string

// FaultNone is the clean, fully recoverable success scenario; FaultTruncated
// feeds a cut MXF header to the metadata stage while keeping the full input
// stored on disk for recovery.
const (
	FaultNone      Fault = ""
	FaultTruncated Fault = "truncated"
)

// Fixture is the complete generated input set for one pipeline run.
type Fixture struct {
	Seed        int64
	MXF         []byte
	MP4         []byte
	RTPFrames   []rtppkg.CaptureFrame
	Bandwidth   []rtppkg.BandwidthSample
	Fault       Fault
	// TruncatedMXF is the cut copy fed to the parser under FaultTruncated.
	TruncatedMXF    []byte
	TruncatedOffset int64
	VideoFrames     int
	AudioFragments  int
	SubtitleCues    int
}

// lcg is the same deterministic generator used by the renderer.
func lcg(state uint64) uint64 { return state*6364136223846793005 + 1442695040888963407 }

// Generate builds a fixture. With FaultTruncated the MXF working copy is cut
// at a deterministic mid-header offset; the uncut MXF remains in MXF.
func Generate(seed int64, fault Fault) (*Fixture, error) {
	f := &Fixture{
		Seed: seed, Fault: fault,
		VideoFrames: 60, AudioFragments: 2, SubtitleCues: 4,
	}
	f.MXF = buildMXF()
	f.MP4 = buildMP4()
	f.RTPFrames, f.Bandwidth = buildRTP(seed)
	if fault == FaultTruncated {
		// Cut inside the header region, after the magic and a few records.
		cut := int64(120)
		if cut > int64(len(f.MXF)-1) {
			cut = int64(len(f.MXF) - 1)
		}
		f.TruncatedOffset = cut
		f.TruncatedMXF = append([]byte(nil), f.MXF[:cut]...)
	}
	return f, nil
}

// --- MXF line-oriented interchange fixture --------------------------------

func buildMXF() []byte {
	tmpl := "MXF1\n" +
		"LENGTH=%010d\n" +
		"TIMESCALE=1000\n" +
		"DURATION=2000\n" +
		"ROTATION=90\n" +
		"CROP=10,10,30,30\n" +
		"COLOR=9,16,9,1\n" +
		"TRACK=kind=video id=1 timescale=30 duration=60 width=1920 height=1080 rotation=90 crop=10,10,30,30 color=9,16,9,1\n" +
		"TRACK=kind=audio id=2 timescale=48000 duration=96000 channels=2 samplerate=48000\n" +
		"TRACK=kind=subtitle id=3 timescale=1000 duration=2000 language=zh\n" +
		"END\n"
	raw := []byte(fmt.Sprintf(tmpl, 0))
	// Rewrite the fixed-width LENGTH field with the true byte count.
	n := len(raw)
	copy(raw[len("MXF1\nLENGTH="):len("MXF1\nLENGTH=")+10],
		[]byte(fmt.Sprintf("%010d", n)))
	return raw
}

// --- MP4 minimal box construction -----------------------------------------

func box(typ string, payload ...[]byte) []byte {
	n := 0
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 8+n)
	binary.BigEndian.PutUint32(b, uint32(8+n))
	copy(b[4:8], typ)
	off := 8
	for _, p := range payload {
		copy(b[off:], p)
		off += len(p)
	}
	return b
}

func fullBox(ver uint8, body []byte) []byte { return append([]byte{ver, 0, 0, 0}, body...) }
func u32(v uint32) []byte                  { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u16(v uint16) []byte                  { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func buildMP4() []byte {
	// Movie header: 1000 units/s, 2000 units = 2 s.
	mvhdBody := concat(make([]byte, 8), u32(1000), u32(2000), make([]byte, 80))
	mvhd := box("mvhd", fullBox(0, mvhdBody))

	// Video tkhd: explicit 180 rotation, conflicting with MXF's 90.
	const unit = 1 << 16
	rot180 := [9]int32{-unit, 0, 0, 0, -unit, 0, 0, 0, unit}
	tkhdBody := concat(make([]byte, 8), u32(1), u32(0), u32(60), make([]byte, 16))
	for _, v := range rot180 {
		tkhdBody = append(tkhdBody, u32(uint32(v))...)
	}
	tkhdBody = concat(tkhdBody, u32(1920<<16), u32(1080<<16))
	tkhd := box("tkhd", fullBox(0, tkhdBody))

	mdhd := box("mdhd", fullBox(0, concat(make([]byte, 8), u32(30), u32(60), make([]byte, 4))))
	hdlrBody := concat(make([]byte, 4), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00"))
	hdlr := box("hdlr", fullBox(0, hdlrBody))

	avc1 := make([]byte, 78)
	binary.BigEndian.PutUint16(avc1[24:26], 1920)
	binary.BigEndian.PutUint16(avc1[26:28], 1080)
	clapRaw := make([]byte, 32)
	binary.BigEndian.PutUint32(clapRaw[16:], 30)
	binary.BigEndian.PutUint32(clapRaw[20:], 1)
	binary.BigEndian.PutUint32(clapRaw[24:], 10)
	binary.BigEndian.PutUint32(clapRaw[28:], 1)
	colrBody := concat([]byte("nclx"), u16(1), u16(1), u16(1), []byte{0x80, 0x00})
	stsd := box("stsd", fullBox(0, concat(u32(1), box("avc1", concat(avc1, box("clap", clapRaw), box("colr", colrBody))))))
	videoMdia := box("mdia", mdhd, hdlr, box("minf", box("stbl", stsd)))
	videoTrak := box("trak", tkhd, videoMdia)

	// Audio track, 48 kHz stereo, 2 s.
	aTkhd := concat(make([]byte, 8), u32(2), u32(0), u32(96000), make([]byte, 60))
	aMdhd := box("mdhd", fullBox(0, concat(make([]byte, 8), u32(48000), u32(96000), make([]byte, 4))))
	aHdlrBody := concat(make([]byte, 4), []byte("soun"), make([]byte, 12), []byte("SoundHandler\x00"))
	aHdlr := box("hdlr", fullBox(0, aHdlrBody))
	mp4a := make([]byte, 36)
	binary.BigEndian.PutUint16(mp4a[16:18], 2)
	binary.BigEndian.PutUint32(mp4a[24:28], 48000<<16)
	aStsd := box("stsd", fullBox(0, concat(u32(1), box("mp4a", mp4a))))
	audioMdia := box("mdia", aMdhd, aHdlr, box("minf", box("stbl", aStsd)))
	audioTrak := box("trak", box("tkhd", fullBox(0, aTkhd)), audioMdia)

	moov := box("moov", mvhd, videoTrak, audioTrak)
	return concat(box("ftyp", concat([]byte("isom"), make([]byte, 4))), moov)
}

// --- RTP capture with duplicates, reordering, burst loss and wrap ----------

const (
	rtpStartSeq = 65500
	rtpCount    = 140
)

func buildRTP(seed int64) ([]rtppkg.CaptureFrame, []rtppkg.BandwidthSample) {
	// Present sequence numbers: full run minus a 3-packet burst, plus dups.
	var seqs []uint16
	burstLost := map[uint16]bool{65530: true, 65531: true, 65532: true}
	// Duplicate one pre-wrap and one post-wrap sequence (extended 65600 ==
	// uint16 64 after the 16-bit counter rolls over).
	dups := map[uint16]bool{65510: true, 64: true}
	for i := 0; i < rtpCount; i++ {
		seq := uint16((rtpStartSeq + i) & 0xFFFF)
		if burstLost[seq] {
			continue
		}
		seqs = append(seqs, seq)
		if dups[seq] {
			seqs = append(seqs, seq)
		}
	}
	// Deterministic arrival reordering, including across the 16-bit wrap.
	swap := func(a, b uint16) {
		ia, ib := indexOf(seqs, a), indexOf(seqs, b)
		if ia >= 0 && ib >= 0 {
			seqs[ia], seqs[ib] = seqs[ib], seqs[ia]
		}
	}
	swap(65520, 65521)
	swap(65535, 0) // wrap-adjacent pair
	swap(64, 65)   // post-wrap pair (extended 65600/65601)

	var frames []rtppkg.CaptureFrame
	var bw []rtppkg.BandwidthSample
	var offset int64
	state := uint64(seed) + 1
	for arrival, seq := range seqs {
		state = lcg(state)
		payloadLen := 40 + int(state%60)
		payload := make([]byte, payloadLen)
		for i := range payload {
			state = lcg(state)
			payload[i] = byte(state >> 56)
		}
		pkt := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(arrival) * 160, SSRC: 42},
			Payload: payload,
		}
		data, err := pkt.Marshal()
		if err != nil {
			panic(err) // fixture generation is deterministic and must not fail
		}
		frames = append(frames, rtppkg.CaptureFrame{Data: data, ByteOffset: offset, Arrival: arrival})
		offset += int64(len(data))
		// Varying bandwidth: triangle wave between 80 kB/s and 7 MB/s.
		span := 7_000_000 - 80_000
		phase := float64(arrival%24) / 24.0
		tri := 1 - 2*abs(phase-0.5)
		bps := int64(80_000 + float64(span)*tri)
		bw = append(bw, rtppkg.BandwidthSample{BytesPerSecond: bps})
	}
	return frames, bw
}

func indexOf(s []uint16, v uint16) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
