// Package fixture builds fully synthetic, deterministic Matroska-like EBML
// streams and RTP captures for the recovery pipeline. No external files are
// read; every byte is produced from the supplied RNG seed.
package fixture

import (
	"encoding/binary"
	"math"
	"math/rand"

	"archivectl/internal/ebml"

	"github.com/pion/rtp"
)

// Output file names for the generated fixture set.
const (
	NormalInterleave = "normal_interleaved.mkv"
	UnknownLength    = "unknown_length.mkv"
	TailTruncated    = "tail_truncated.mkv"
	CorruptCluster   = "corrupt_cluster.mkv"
	IncompleteIndex  = "incomplete_index.mkv"
	RTPCapture       = "rtp_capture.bin"
	Manifest         = "manifest.txt"
)

// Set is the complete generated fixture bundle.
type Set struct {
	Normal     []byte
	Unknown    []byte
	Truncated  []byte
	Corrupt    []byte
	Incomplete []byte
	RTP        []byte
	Seed       int64
	LossWindow int
	RTPPackets int
}

// Generate deterministically builds the whole fixture set.
func Generate(seed int64, lossWindow int) *Set {
	rng := rand.New(rand.NewSource(seed))
	unknown := BuildUnknownLength(rng)
	fullUnknown := unknown
	s := &Set{
		Normal:     BuildInterleaved(rng),
		Unknown:    unknown,
		Truncated:  fullUnknown[:len(fullUnknown)-4],
		Corrupt:    BuildCorruptCluster(rng),
		Incomplete: BuildIncompleteIndex(rng),
		Seed:       seed,
		LossWindow: lossWindow,
	}
	rtpBytes, pktCount := BuildRTPCapture(rng, lossWindow)
	s.RTP = rtpBytes
	s.RTPPackets = pktCount
	return s
}

// BuildInterleaved builds a healthy explicit-size multi-track stream.
func BuildInterleaved(rng *rand.Rand) []byte {
	var seg []byte
	seg = append(seg, infoElement(2_000_000_000)...)
	seg = append(seg, Cluster(0, rng, false)...)
	seg = append(seg, Cluster(1000, rng, false)...)
	return ebml.ElementBytes(ebml.IDSegment, seg)
}

// BuildUnknownLength builds a stream whose Segment and Clusters are
// unknown-length.
func BuildUnknownLength(rng *rand.Rand) []byte {
	var seg []byte
	seg = append(seg, infoElement(2_000_000_000)...)
	seg = append(seg, Cluster(0, rng, true)...)
	seg = append(seg, Cluster(1000, rng, true)...)
	return ebml.ElementBytesUnknown(ebml.IDSegment, seg)
}

// BuildCorruptCluster tampers with a block's size VINT inside the tail
// cluster so the parser hits an out-of-range length. Earlier blocks in the
// same cluster remain intact and salvageable.
func BuildCorruptCluster(rng *rand.Rand) []byte {
	good := BuildInterleaved(rng)
	out := append([]byte(nil), good...)

	// Locate the last SimpleBlock header (ID byte 0xA3) and overwrite its
	// size VINT with a large explicit length that cannot fit the buffer.
	idPos := -1
	for i := 0; i+1 < len(out); i++ {
		if out[i] == 0xA3 {
			idPos = i
		}
	}
	if idPos < 0 {
		panic("fixture: no SimpleBlock found to corrupt")
	}
	sizePos := idPos + 1
	// 0x4x is a 4-byte VINT (marker in high nibble); set the largest value
	// the remaining width expresses, guaranteeing an overrun.
	for j := sizePos; j < sizePos+4 && j < len(out); j++ {
		out[j] = 0x4F
	}
	out[sizePos] = 0x4F
	return out
}

// BuildIncompleteIndex builds a valid stream with the seek index absent and
// a two-byte tail cut, forcing index reconstruction.
func BuildIncompleteIndex(rng *rand.Rand) []byte {
	full := BuildInterleaved(rng)
	return full[:len(full)-2]
}

// Cluster assembles one Cluster with interleaved video/audio blocks.
func Cluster(timecodeMs int64, rng *rand.Rand, unknown bool) []byte {
	var payload []byte
	payload = append(payload, ebml.ElementBytes(ebml.IDTimecode,
		ebml.UintBytes(uint64(timecodeMs)))...)
	payload = append(payload, Block(1, 0, true, rng, 6)...)
	payload = append(payload, Block(2, 5, false, rng, 4)...)
	payload = append(payload, Block(1, 40, false, rng, 6)...)
	payload = append(payload, Block(2, 25, false, rng, 4)...)
	if unknown {
		return ebml.ElementBytesUnknown(ebml.IDCluster, payload)
	}
	return ebml.ElementBytes(ebml.IDCluster, payload)
}

// Block builds one SimpleBlock with opaque random frame data.
func Block(track uint32, relTc int16, key bool, rng *rand.Rand, size int) []byte {
	data := make([]byte, size)
	_, _ = rng.Read(data)
	var p []byte
	p = append(p, ebml.EncodeVINT(uint64(track), 0)...)
	p = append(p, byte(relTc>>8), byte(relTc))
	var flags byte
	if key {
		flags = 0x80
	}
	p = append(p, flags)
	p = append(p, data...)
	return ebml.ElementBytes(ebml.IDSimpleBlock, p)
}

// BuildRTPCapture serializes length-prefixed RTP packets with a fixed loss
// set and adjacent swaps for out-of-order delivery.
func BuildRTPCapture(rng *rand.Rand, window int) ([]byte, int) {
	const total = 60
	lost := map[uint16]bool{7: true, 19: true, 33: true, 51: true}
	var seq []uint16
	for s := uint16(0); s < total; s++ {
		if !lost[s] {
			seq = append(seq, s)
		}
	}
	for i := 0; i+1 < len(seq); i += 7 {
		seq[i], seq[i+1] = seq[i+1], seq[i]
	}
	// Inject a duplicate delivery of an early surviving packet.
	dupAt := 0
	if len(seq) > 3 {
		dupAt = 3
	}
	seq = append(seq[:dupAt+1],
		append([]uint16{seq[dupAt]}, seq[dupAt+1:]...)...)
	var buf []byte
	for _, sn := range seq {
		p := &rtp.Packet{Header: rtp.Header{SequenceNumber: sn, PayloadType: 96}}
		p.Payload = make([]byte, 12)
		_, _ = rng.Read(p.Payload)
		raw, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		buf = append(buf, byte(len(raw)>>8), byte(len(raw)))
		buf = append(buf, raw...)
	}
	buf = append(buf, byte(window>>8), byte(window))
	return buf, len(seq)
}

// DecodeRTPCapture reverses BuildRTPCapture, returning packets and the window.
func DecodeRTPCapture(raw []byte) ([]*rtp.Packet, int, error) {
	var packets []*rtp.Packet
	i := 0
	for i+2 < len(raw) {
		l := int(raw[i])<<8 | int(raw[i+1])
		i += 2
		if i+l > len(raw) {
			break
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(raw[i : i+l]); err != nil {
			return nil, 0, err
		}
		packets = append(packets, p)
		i += l
	}
	window := 0
	if i+2 == len(raw) {
		window = int(raw[i])<<8 | int(raw[i+1])
	}
	return packets, window, nil
}

func infoElement(durationNs int64) []byte {
	var info []byte
	info = append(info, ebml.ElementBytes(ebml.IDTimecodeScale,
		ebml.UintBytes(1_000_000))...)
	info = append(info, ebml.ElementBytes(ebml.IDDuration,
		float64Bytes(float64(durationNs)/1_000_000))...)
	return ebml.ElementBytes(ebml.IDInfo, info)
}

func float64Bytes(f float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(f))
	return b
}
