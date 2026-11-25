// Package fixture generates the self-contained sample data the regression
// command consumes: an MP4 container with timed metadata (subtitle, chapter,
// ID3, emsg) and an H264 RTP video trace carrying SEI. No external files or
// network services are involved.
package fixture

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	prtp "github.com/pion/rtp"
)

// Names used in the generated directory layout.
const (
	SampleDir    = "sample"
	TruncatedDir = "truncated"
	MP4Name      = "sample.mp4"
	TraceName    = "video.trace"
	TruncName    = "truncated.mp4"
)

// Layout records the generated paths.
type Layout struct {
	Root      string
	SampleMP4 string
	Trace     string
	Truncated string
}

// Generate writes the fixtures under root and returns their paths. The sample
// directory holds a valid MP4 and a matching RTP trace; the truncated
// directory holds an MP4 whose final emsg box is cut short.
func Generate(root string) (*Layout, error) {
	sampleDir := filepath.Join(root, SampleDir)
	truncDir := filepath.Join(root, TruncatedDir)
	for _, d := range []string{sampleDir, truncDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	lay := &Layout{
		Root:      root,
		SampleMP4: filepath.Join(sampleDir, MP4Name),
		Trace:     filepath.Join(sampleDir, TraceName),
		Truncated: filepath.Join(truncDir, TruncName),
	}
	mp4Bytes, err := buildMP4()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(lay.SampleMP4, mp4Bytes, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", lay.SampleMP4, err)
	}
	traceBytes, err := buildTrace()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(lay.Trace, traceBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", lay.Trace, err)
	}
	trunc, err := truncateLastBox(mp4Bytes)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(lay.Truncated, trunc, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", lay.Truncated, err)
	}
	return lay, nil
}

// track is one metadata track written into the MP4.
type track struct {
	name      string
	handler   [4]byte
	timescale uint32
	samples   [][]byte
	durations []uint32 // ticks, parallel to samples
}

// buildMP4 lays out ftyp | mdat | moov | emsg | emsg. The media data comes
// before moov so every chunk offset is known while moov is being written; no
// second pass or offset patching is needed.
func buildMP4() ([]byte, error) {
	tracks := []track{
		{
			name: "subtitle", handler: [4]byte{'s', 'b', 't', 'l'}, timescale: 1000,
			samples: [][]byte{
				textSample("弱网轨迹对齐"),
				textSample("second cue survives reordering"),
			},
			durations: []uint32{600, 700},
		},
		{
			name: "chapter", handler: [4]byte{'t', 'e', 'x', 't'}, timescale: 1000,
			samples: [][]byte{
				textSample("chapter-1 intro"),
				textSample("chapter-2 drop check"),
			},
			durations: []uint32{1200, 800},
		},
		{
			name: "id3", handler: [4]byte{'m', 'e', 't', 'a'}, timescale: 48000,
			samples: [][]byte{
				id3Sample("RTPREGRESS", "id3-cue-0"),
				id3Sample("RTPREGRESS", "id3-cue-1"),
			},
			durations: []uint32{9600, 38400},
		},
	}

	var out bytes.Buffer
	writeFtyp(&out)

	// mdat holds one chunk per track: all of that track's samples
	// concatenated. Offsets are computed as the payload is appended.
	mdatPayloadStart := int64(out.Len()) + 8
	chunkOffsets := make([]int64, len(tracks))
	mdatBody := &bytes.Buffer{}
	for i, tr := range tracks {
		chunkOffsets[i] = mdatPayloadStart + int64(mdatBody.Len())
		for _, s := range tr.samples {
			mdatBody.Write(s)
		}
	}
	mdatHdr := make([]byte, 8)
	binary.BigEndian.PutUint32(mdatHdr[0:4], uint32(8+mdatBody.Len()))
	copy(mdatHdr[4:8], "mdat")
	out.Write(mdatHdr)
	out.Write(mdatBody.Bytes())

	writeMoov(&out, tracks, chunkOffsets)

	// Two emsg boxes at 900ms and 1800ms on a 1000-tick clock.
	if err := writeEmsg(&out, "urn:test:emsg:1", 900, []byte("emsg-regress-1")); err != nil {
		return nil, err
	}
	if err := writeEmsg(&out, "urn:test:emsg:2", 1800, []byte("emsg-regress-2")); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeFtyp(w *bytes.Buffer) {
	body := &bytes.Buffer{}
	body.WriteString("isom")
	body.Write(be32(0x200))
	body.WriteString("isom")
	body.WriteString("mp42")
	writeBox(w, "ftyp", body.Bytes())
}

func writeMoov(w *bytes.Buffer, tracks []track, chunkOffsets []int64) {
	moov := &bytes.Buffer{}
	writeFullBox(moov, "mvhd", 0, 0, mvhdBody(len(tracks)))
	for i, tr := range tracks {
		trak := &bytes.Buffer{}
		writeFullBox(trak, "tkhd", 0, 0x000007, tkhdBody())
		mdia := &bytes.Buffer{}
		writeFullBox(mdia, "mdhd", 0, 0, mdhdBody(tr.timescale, sumDurations(tr.durations)))
		writeFullBox(mdia, "hdlr", 0, 0, hdlrBody(tr.name, tr.handler))
		minf := &bytes.Buffer{}
		writeFullBox(minf, "nmhd", 0, 0, nil)
		stbl := &bytes.Buffer{}
		writeStbl(stbl, tr, chunkOffsets[i])
		writeBox(minf, "stbl", stbl.Bytes())
		writeBox(mdia, "minf", minf.Bytes())
		writeBox(trak, "mdia", mdia.Bytes())
		writeBox(moov, "trak", trak.Bytes())
	}
	writeBox(w, "moov", moov.Bytes())
}

func writeStbl(w *bytes.Buffer, tr track, chunkOffset int64) {
	// An empty stsd is structurally valid and irrelevant to chunk mapping.
	writeFullBox(w, "stsd", 0, 0, be32(0))
	// stts: one run per distinct per-sample duration, but the simplest
	// correct encoding is one entry per sample.
	stts := be32(uint32(len(tr.durations)))
	for _, d := range tr.durations {
		stts = append(stts, be32(1)...)
		stts = append(stts, be32(d)...)
	}
	writeFullBox(w, "stts", 0, 0, stts)
	// stsz: sample_size 0 means "use the per-sample table".
	stsz := be32(0)
	stsz = append(stsz, be32(uint32(len(tr.samples)))...)
	for _, smp := range tr.samples {
		stsz = append(stsz, be32(uint32(len(smp)))...)
	}
	writeFullBox(w, "stsz", 0, 0, stsz)
	// stsc: one sample-per-chunk run covering the track's single chunk.
	stsc := be32(1)
	stsc = append(stsc, be32(1)...) // first_chunk
	stsc = append(stsc, be32(uint32(len(tr.samples)))...)
	stsc = append(stsc, be32(1)...) // sample_description_index
	writeFullBox(w, "stsc", 0, 0, stsc)
	// stco: the single chunk's absolute offset.
	stco := append(be32(1), be32(uint32(chunkOffset))...)
	writeFullBox(w, "stco", 0, 0, stco)
}

func sumDurations(d []uint32) uint32 {
	var s uint32
	for _, v := range d {
		s += v
	}
	return s
}

func mvhdBody(trackCount int) []byte {
	b := make([]byte, 0, 100)
	b = append(b, be32(0)...) // creation_time
	b = append(b, be32(0)...) // modification_time
	b = append(b, be32(1000)...)
	b = append(b, be32(0)...)
	b = append(b, be32(0x00010000)...)
	b = append(b, be16(0x0100)...)
	b = append(b, make([]byte, 10)...)
	b = append(b, matrix()...)
	b = append(b, make([]byte, 24)...)
	b = append(b, be32(uint32(trackCount+1))...)
	return b
}

func tkhdBody() []byte {
	b := make([]byte, 0, 84)
	b = append(b, be32(0)...)
	b = append(b, make([]byte, 4)...)
	b = append(b, be32(0)...)
	b = append(b, make([]byte, 8)...)
	b = append(b, be16(0)...)
	b = append(b, be16(0)...)
	b = append(b, be16(0)...)
	b = append(b, be16(0)...)
	b = append(b, matrix()...)
	b = append(b, be32(0)...)
	b = append(b, be32(0)...)
	return b
}

func mdhdBody(timescale, duration uint32) []byte {
	b := make([]byte, 0, 20)
	b = append(b, be32(0)...)
	b = append(b, be32(0)...)
	b = append(b, be32(timescale)...)
	b = append(b, be32(duration)...)
	b = append(b, make([]byte, 4)...)
	return b
}

func hdlrBody(name string, handler [4]byte) []byte {
	b := make([]byte, 0, 28)
	b = append(b, be32(0)...)
	b = append(b, handler[:]...)
	b = append(b, make([]byte, 12)...)
	b = append(b, name...)
	b = append(b, 0)
	return b
}

func matrix() []byte {
	v := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	b := make([]byte, 0, 36)
	for _, x := range v {
		b = append(b, be32(x)...)
	}
	return b
}

func writeBox(w *bytes.Buffer, typ string, body []byte) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(body)))
	copy(hdr[4:8], typ)
	w.Write(hdr)
	w.Write(body)
}

func writeFullBox(w *bytes.Buffer, typ string, version uint8, flags uint32, body []byte) {
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(12+len(body)))
	copy(hdr[4:8], typ)
	hdr[8] = version
	hdr[9] = byte(flags >> 16)
	hdr[10] = byte(flags >> 8)
	hdr[11] = byte(flags)
	w.Write(hdr)
	w.Write(body)
}

func writeEmsg(w *bytes.Buffer, scheme string, delta uint32, msg []byte) error {
	body := &bytes.Buffer{}
	body.WriteString(scheme)
	body.WriteByte(0)
	body.WriteString("1") // value
	body.WriteByte(0)
	body.Write(be32(1000)) // timescale
	body.Write(be32(delta))
	body.Write(be32(40)) // event_duration
	body.Write(be32(1))  // id
	body.Write(msg)
	writeFullBox(w, "emsg", 0, 0, body.Bytes())
	return nil
}

func textSample(text string) []byte {
	b := make([]byte, 0, 2+len(text)+4)
	b = append(b, be16(uint16(len(text)))...)
	b = append(b, text...)
	b = append(b, make([]byte, 4)...) // unused style fields
	return b
}

// id3Sample builds an ID3v2.3 tag holding one UTF-8 TXXX frame.
func id3Sample(desc, value string) []byte {
	frameBody := []byte{3} // UTF-8
	frameBody = append(frameBody, desc...)
	frameBody = append(frameBody, 0)
	frameBody = append(frameBody, value...)
	frameBody = append(frameBody, 0)

	frame := &bytes.Buffer{}
	frame.WriteString("TXXX")
	frame.Write(be32(uint32(len(frameBody))))
	frame.Write(be16(0)) // frame flags: 10-byte header total
	frame.Write(frameBody)

	// ID3v2.3 synchsafe tag size counts the frames only; the 10-byte header
	// is separate, so every bound check uses 10+tagSize.
	tagSize := frame.Len()
	if tagSize >= 1<<21 {
		panic("ID3 fixture tag too large")
	}
	hdr := make([]byte, 10)
	hdr[0], hdr[1], hdr[2] = 'I', 'D', '3'
	hdr[3] = 3 // ID3v2.3
	hdr[4] = 0 // no flags
	hdr[6] = byte(tagSize >> 21 & 0x7f)
	hdr[7] = byte(tagSize >> 14 & 0x7f)
	hdr[8] = byte(tagSize >> 7 & 0x7f)
	hdr[9] = byte(tagSize & 0x7f)
	out := append(hdr, frame.Bytes()...)
	return out
}

func be16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// truncateLastBox cuts the final box after its header so the box declares
// more payload than the file holds.
func truncateLastBox(data []byte) ([]byte, error) {
	pos, last, lastSize := 0, -1, 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if size < 8 || pos+size > len(data) {
			break
		}
		last, lastSize = pos, size
		pos += size
	}
	if last < 0 {
		return nil, fmt.Errorf("no boxes to truncate")
	}
	_ = lastSize
	return data[:last+12], nil
}

// seiPayload builds a user_data_unregistered SEI NAL unit. The first 16
// bytes of the unregistered payload are the scheme UUID, the rest is text.
func seiPayload(text string) []byte {
	uuid := []byte("RTPREGRESS-SEI01") // exactly 16 bytes
	body := append(uuid, []byte(text)...)
	nal := []byte{0x26} // nal_ref_idc=1, type=6 (SEI)
	// payload type 5, then payload size in 0xff-extended bytes.
	nal = append(nal, 5)
	size := len(body)
	for size >= 255 {
		nal = append(nal, 0xff)
		size -= 255
	}
	nal = append(nal, byte(size))
	nal = append(nal, body...)
	// rbsp trailing bits
	nal = append(nal, 0x80)
	return nal
}

// nonSEINAL builds a filler NAL so the trace contains ordinary frames too.
func nonSEINAL(fill int) []byte {
	nal := make([]byte, 1+fill)
	nal[0] = 0x0a // nal_ref_idc=0, type=10 (end of sequence-ish filler)
	for i := 1; i < len(nal); i++ {
		nal[i] = byte(i)
	}
	return nal
}

// buildTrace produces the H264 RTP trace. Arrival order deliberately includes
// one missing sequence number (loss), one duplicate and one late packet, so
// the reorder path is exercised on the sample itself.
func buildTrace() ([]byte, error) {
	type pkt struct {
		seq     uint16
		ts      uint32
		payload []byte
		arrival int64
	}
	// Sequence numbers 100..106 are used. Packet 102 arrives only after its
	// slot has been committed (so it must be reported, not applied) and
	// packet 104 never arrives at all (so it is recorded as loss).
	pkts := []pkt{
		{100, 0, nonSEINAL(8), 0},
		{101, 9000, nonSEINAL(6), 8_000_000},
		{103, 27000, seiPayload("sei-cue-1"), 300_000_000},
		{105, 45000, nonSEINAL(4), 450_000_000},
		{106, 54000, seiPayload("sei-cue-2"), 540_000_000},
		{102, 18000, nonSEINAL(5), 600_000_000},            // late: fills a committed hole
		{103, 27000, seiPayload("sei-cue-1"), 620_000_000}, // duplicate
	}
	var b bytes.Buffer
	b.WriteString("# offline RTP trace, sequence numbers wrap-extended\n")
	b.WriteString("track video\n")
	b.WriteString("timescale 90000\n")
	b.WriteString("codec h264\n")
	b.WriteString("payload 96\n")
	for _, p := range pkts {
		rp := &prtp.Packet{}
		rp.Version = 2
		rp.PayloadType = 96
		rp.SequenceNumber = p.seq
		rp.Timestamp = p.ts
		rp.SSRC = 0x5eed0001
		rp.Payload = p.payload
		raw, err := rp.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal RTP packet %d: %w", p.seq, err)
		}
		b.WriteString("rtp " + hex.EncodeToString(raw) + " " + strconv.FormatInt(p.arrival, 10) + "\n")
	}
	return b.Bytes(), nil
}
