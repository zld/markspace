package mediakernel

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"

	"github.com/pion/rtp"
)

// Fixture is a generated sample: the media bytes plus, for corrupt samples,
// the expected error code and the generator-recorded byte offset.
type Fixture struct {
	Data      []byte
	Tracks    int
	ErrCode   Code
	ErrOffset int64
}

type builder struct{ buf bytes.Buffer }

func (b *builder) pos() int64 { return int64(b.buf.Len()) }

func (b *builder) id(id uint32) {
	n := 1
	for n < 4 && uint64(id) >= (uint64(1)<<(8*n)) {
		n++
	}
	for i := n - 1; i >= 0; i-- {
		b.buf.WriteByte(byte(id >> (8 * i)))
	}
}

func (b *builder) size(v uint64) {
	for w := uint(1); w <= 8; w++ {
		if v <= (uint64(1)<<(7*w))-2 {
			val := v | (uint64(1) << (7 * w))
			for i := int(w) - 1; i >= 0; i-- {
				b.buf.WriteByte(byte(val >> (8 * i)))
			}
			return
		}
	}
}

func (b *builder) openUnknown(id uint32) { b.id(id); b.buf.WriteByte(0xFF) }

func leafb(id uint32, data []byte) []byte {
	var b builder
	b.id(id)
	b.size(uint64(len(data)))
	b.buf.Write(data)
	return b.buf.Bytes()
}

func uintLeaf(id uint32, v uint64) []byte {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], v)
	n := 8
	for n > 1 && data[8-n] == 0 {
		n--
	}
	return leafb(id, data[8-n:])
}

func masterK(id uint32, children ...[]byte) []byte {
	var b builder
	b.id(id)
	n := 0
	for _, c := range children {
		n += len(c)
	}
	b.size(uint64(n))
	for _, c := range children {
		b.buf.Write(c)
	}
	return b.buf.Bytes()
}

func masterU(id uint32, children ...[]byte) []byte {
	var b builder
	b.openUnknown(id)
	for _, c := range children {
		b.buf.Write(c)
	}
	return b.buf.Bytes()
}

func simpleBlock(track uint64, rel int16, payload []byte) []byte {
	var b builder
	b.size(track)
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(rel))
	b.buf.Write(tc[:])
	b.buf.WriteByte(0)
	b.buf.Write(payload)
	return leafb(IDSimpleBlock, b.buf.Bytes())
}

func rtpPacket(seq uint16, ts uint32, ssrc uint32, payload []byte) []byte {
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96,
		SequenceNumber: seq, Timestamp: ts, SSRC: ssrc}, Payload: payload}
	data, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return data
}

const (
	fixSampleRate = 16000
	fixBlockMs    = 20
	fixSamples    = fixSampleRate * fixBlockMs / 1000 // 320
)

// pcmPayload returns deterministic PCM16LE content for a block. The region
// [200,360) ms is digital silence; track 1 content in [480,560) ms duplicates
// [80,160) ms exactly.
func pcmPayload(seed int64, track uint64, ms int64) []byte {
	if ms >= 200 && ms < 360 {
		return make([]byte, fixSamples*2)
	}
	key := ms
	if track == 1 && ms >= 480 && ms < 560 {
		key = ms - 400
	}
	rng := rand.New(rand.NewSource(seed*7919 + int64(track)*104729 + key))
	freq := 300 + 40*float64(track) + float64(key%7)*10
	out := make([]byte, fixSamples*2)
	for i := 0; i < fixSamples; i++ {
		t := float64(key)/1000 + float64(i)/fixSampleRate
		v := 8000*math.Sin(2*math.Pi*freq*t) + float64(rng.Intn(200)-100)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v)))
	}
	return out
}

func trackEntry(n uint64) []byte {
	return masterK(IDTrackEntry,
		uintLeaf(IDTrackNumber, n),
		uintLeaf(IDTrackType, 2),
		leafb(IDCodecID, []byte("RTP/PCM16LE")),
		uintLeaf(IDDefaultDuration, 20_000_000))
}

func fileHead(b *builder) {
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.openUnknown(IDSegment)
	b.buf.Write(masterK(IDInfo, uintLeaf(IDTimecodeScale, 1_000_000)))
	b.buf.Write(masterK(IDTracks, trackEntry(1), trackEntry(2)))
}

type blockSpec struct {
	track uint64
	rel   int64 // ms offset within cluster
}

// writeCluster appends one cluster with interleaved blocks of both tracks.
func writeCluster(seg *builder, baseMs int64, seq map[uint64]uint16, seed int64, specs []blockSpec) {
	var cl builder
	cl.buf.Write(uintLeaf(IDTimecode, uint64(baseMs)))
	for _, s := range specs {
		ms := baseMs + s.rel
		pkt := rtpPacket(seq[s.track], uint32(ms*16), uint32(1000+s.track), pcmPayload(seed, s.track, ms))
		seq[s.track]++
		cl.buf.Write(simpleBlock(s.track, int16(s.rel), pkt))
	}
	seg.buf.Write(masterK(IDCluster, cl.buf.Bytes()))
}

// interleaveOrder alternates track order per timestamp to exercise stable
// sorting while keeping each track's timestamps non-decreasing.
func interleaveOrder(i int64) []blockSpec {
	if i%2 == 0 {
		return []blockSpec{{1, 0}, {2, 0}, {1, 20}, {2, 20}}
	}
	return []blockSpec{{2, 0}, {1, 0}, {2, 20}, {1, 20}}
}

// BuildInterleaved builds a valid two-track WebM with unknown-size segment,
// interleaved clusters, a silence region and duplicated content.
func BuildInterleaved(seed int64) Fixture {
	var seg builder
	seg.buf.Write(masterK(IDInfo, uintLeaf(IDTimecodeScale, 1_000_000)))
	seg.buf.Write(masterK(IDTracks, trackEntry(1), trackEntry(2)))
	seq := map[uint64]uint16{}
	for i := int64(0); i < 16; i++ {
		writeCluster(&seg, i*40, seq, seed, interleaveOrder(i))
	}
	var b builder
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.buf.Write(masterU(IDSegment, seg.buf.Bytes()))
	return Fixture{Data: b.buf.Bytes(), Tracks: 2}
}

// BuildTruncatedVINT builds a sample whose final block size VINT declares
// width 4 but the input ends after 2 bytes.
func BuildTruncatedVINT(seed int64) Fixture {
	var b builder
	fileHead(&b)
	seq := map[uint64]uint16{}
	writeCluster(&b, 0, seq, seed, interleaveOrder(0))
	b.openUnknown(IDCluster)
	b.buf.Write(uintLeaf(IDTimecode, 40))
	b.id(IDSimpleBlock)
	b.buf.Write([]byte{0x10, 0x00}) // width-4 size VINT, only 2 of 4 bytes
	return Fixture{Data: b.buf.Bytes(), Tracks: 2,
		ErrCode: ErrTruncatedHeader, ErrOffset: b.pos()}
}

// BuildInvalidVINT builds a sample with a zero VINT first byte.
func BuildInvalidVINT(seed int64) Fixture {
	var b builder
	fileHead(&b)
	seq := map[uint64]uint16{}
	writeCluster(&b, 0, seq, seed, interleaveOrder(0))
	off := b.pos()
	b.buf.WriteByte(0x00)
	return Fixture{Data: b.buf.Bytes(), Tracks: 2,
		ErrCode: ErrInvalidVINT, ErrOffset: off}
}

// BuildBadParent builds a sample where a TrackEntry element appears directly
// under Segment, violating the parent boundary.
func BuildBadParent(seed int64) Fixture {
	var b builder
	fileHead(&b)
	off := b.pos()
	b.buf.Write(trackEntry(3))
	return Fixture{Data: b.buf.Bytes(), Tracks: 2,
		ErrCode: ErrParentBoundary, ErrOffset: off}
}

// BuildSizeOverflow builds a sample where a leaf declares a size exceeding
// its parent's end.
func BuildSizeOverflow(seed int64) Fixture {
	var b builder
	b.buf.Write(masterK(IDEBML, uintLeaf(0x4286, 1)))
	b.openUnknown(IDSegment)
	b.id(IDInfo)
	b.size(6) // Info data region: 6 bytes
	b.id(IDTimecodeScale)
	off := b.pos()
	b.size(8) // declares 8 data bytes: ends beyond Info's end
	b.buf.Write([]byte{0, 0, 0, 0})
	return Fixture{Data: b.buf.Bytes(), Tracks: 0,
		ErrCode: ErrSizeOverflow, ErrOffset: off}
}

// BuildTSRegression builds a sample whose fourth cluster moves track 1
// backwards in time.
func BuildTSRegression(seed int64) Fixture {
	var b builder
	fileHead(&b)
	seq := map[uint64]uint16{}
	writeCluster(&b, 0, seq, seed, interleaveOrder(0))
	writeCluster(&b, 40, seq, seed, interleaveOrder(1))
	writeCluster(&b, 80, seq, seed, interleaveOrder(2))
	// Cluster with a regressed timecode: track 1 already reached 100 ms.
	var cl builder
	cl.buf.Write(uintLeaf(IDTimecode, 40))
	pkt := rtpPacket(seq[1], 40*16, 1001, pcmPayload(seed, 1, 40))
	seq[1]++
	cl.buf.Write(simpleBlock(1, 0, pkt))
	b.buf.Write(masterK(IDCluster, cl.buf.Bytes()))
	// The error offset is the regressed block's header offset; recompute by
	// parsing offsets is overkill: record via a marker pass instead.
	off := findBlockOffset(b.buf.Bytes(), 12)
	return Fixture{Data: b.buf.Bytes(), Tracks: 2,
		ErrCode: ErrTSRegression, ErrOffset: off}
}

// findBlockOffset returns the header offset of the n-th SimpleBlock (0-based).
func findBlockOffset(data []byte, n int) int64 {
	p := NewParser(bytes.NewReader(data), int64(len(data)))
	count := 0
	for {
		el, err := p.Next()
		if err != nil {
			return -1
		}
		if el.ID == IDSimpleBlock {
			if count == n {
				return el.HeaderOffset
			}
			count++
		}
		if !el.Master {
			if err := p.Skip(el); err != nil {
				return -1
			}
		}
	}
}
