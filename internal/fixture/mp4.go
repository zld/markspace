// Package fixture generates the self-contained media fixtures used by tests
// and by the demo command. Nothing is read from disk: every MPD, playlist,
// fragmented MP4 file and RTP packet is synthesised here.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"

	"mediadeliver/internal/model"
)

// box writes a 32-bit-size ISO BMFF box.
func box(typ string, payload []byte) []byte {
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(payload)+8))
	copy(hdr[4:], typ)
	buf.Write(hdr[:])
	buf.Write(payload)
	return buf.Bytes()
}

func fullBox(typ string, version uint8, flags uint32, payload []byte) []byte {
	var head [4]byte
	head[0] = version
	head[1] = byte(flags >> 16)
	head[2] = byte(flags >> 8)
	head[3] = byte(flags)
	return box(typ, append(head[:], payload...))
}

func be32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func be64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// FragmentSpec describes one generated moof+mdat pair.
type FragmentSpec struct {
	SequenceNumber uint32
	TrackID        uint32
	BaseDecodeTime uint64
	SampleCount    uint32
	SampleDuration uint32
	SampleSize     uint32
	PayloadSeed    int64
}

// Fragment renders one complete fragment (optional styp + moof + mdat).
func Fragment(spec FragmentSpec, withStyp bool) []byte {
	var out bytes.Buffer
	if withStyp {
		out.Write(box("styp", append([]byte("msdh"), be32(0)...)))
	}
	mfhd := fullBox("mfhd", 0, 0, be32(spec.SequenceNumber))
	tfhd := fullBox("tfhd", 0, 0x020000, append(be32(spec.TrackID), be32(spec.SampleDuration)...))
	tfdt := fullBox("tfdt", 1, 0, be64(spec.BaseDecodeTime))
	trunPayload := append(be32(spec.SampleCount), be32(0)...)
	for i := uint32(0); i < spec.SampleCount; i++ {
		trunPayload = append(trunPayload, be32(spec.SampleDuration)...)
		trunPayload = append(trunPayload, be32(spec.SampleSize)...)
	}
	trun := fullBox("trun", 0, 0x000301, trunPayload)
	traf := box("traf", concat(tfhd, tfdt, trun))
	moof := box("moof", concat(mfhd, traf))
	out.Write(moof)
	payload := samplePayload(spec)
	mdat := box("mdat", payload)
	out.Write(mdat)
	return out.Bytes()
}

func concat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

func samplePayload(spec FragmentSpec) []byte {
	total := int(spec.SampleCount) * int(spec.SampleSize)
	if total <= 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(spec.PayloadSeed + int64(spec.SequenceNumber)))
	out := make([]byte, total)
	rng.Read(out)
	return out
}

// FragmentSize is the byte length of one generated fragment: moof (116
// bytes) plus an mdat whose payload is SampleCount*SampleSize bytes.
const (
	moofSize   = 116
	mdatHeader = 8
)

// FragmentSize returns the encoded length of a single fragment.
func FragmentSize(spec FragmentSpec) int64 {
	return moofSize + mdatHeader + int64(spec.SampleCount)*int64(spec.SampleSize)
}

// SegmentFile renders a full fragmented file with the given fragments.
func SegmentFile(segments []FragmentSpec) []byte {
	var out bytes.Buffer
	for i, spec := range segments {
		out.Write(Fragment(spec, i == 0))
	}
	return out.Bytes()
}

// TruncateBox cuts the final box of data at the given absolute offset,
// simulating a recorder that lost power mid-write.
func TruncateBox(data []byte, offset int64) []byte {
	if offset < 0 || offset > int64(len(data)) {
		offset = int64(len(data))
	}
	return append([]byte(nil), data[:offset]...)
}

// CorruptBoxSize overwrites the 32-bit size field at offset with a value
// that can never be valid.
func CorruptBoxSize(data []byte, offset int64, badSize uint32) []byte {
	out := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(out[offset:offset+4], badSize)
	return out
}

// IncompleteSampleTable rewrites a trun's sample count so it declares more
// samples than it has entries, leaving the moof structurally incomplete.
func IncompleteSampleTable(data []byte, moofOffset int64) ([]byte, error) {
	out := append([]byte(nil), data...)
	idx := bytes.Index(out[moofOffset:], []byte("trun"))
	if idx < 0 {
		return nil, fmt.Errorf("no trun box after offset %d", moofOffset)
	}
	// idx points at the fourcc; version/flags follow, then sample_count.
	countPos := moofOffset + int64(idx) + 8
	if countPos+4 > int64(len(out)) {
		return nil, fmt.Errorf("trun sample count outside the file")
	}
	count := binary.BigEndian.Uint32(out[countPos : countPos+4])
	binary.BigEndian.PutUint32(out[countPos:countPos+4], count+3)
	return out, nil
}

// RTPPacket builds a deterministic RTP packet carrying a media fragment
// slice. It is used to verify packet-offset to file-offset mapping.
type RTPPacket struct {
	SequenceNumber uint16
	Timestamp      uint32
	Marker         bool
	Payload        []byte
	Raw            []byte
}

// RTPStream renders a tiny RTP stream (RFC 6184 style) covering ms of media.
func RTPStream(seed int64, packets int, payloadSize int) ([]RTPPacket, error) {
	rng := rand.New(rand.NewSource(seed))
	out := make([]RTPPacket, 0, packets)
	ts := uint32(0)
	for i := 0; i < packets; i++ {
		payload := make([]byte, payloadSize)
		rng.Read(payload)
		pkt := RTPPacket{
			SequenceNumber: uint16(1000 + i),
			Timestamp:      ts,
			Marker:         i == packets-1,
			Payload:        payload,
		}
		raw, err := marshalRTP(pkt)
		if err != nil {
			return nil, err
		}
		pkt.Raw = raw
		out = append(out, pkt)
		ts += 3000 // 90 kHz clock, 33.3 ms per packet
	}
	return out, nil
}

// marshalRTP writes a minimal RTP version-2 packet with no extensions.
func marshalRTP(p RTPPacket) ([]byte, error) {
	if len(p.Payload) > 1200 {
		return nil, fmt.Errorf("payload %d exceeds the RTP MTU assumption", len(p.Payload))
	}
	var buf bytes.Buffer
	buf.WriteByte(0x80) // version 2, no padding/extension/CSRC
	second := byte(96)  // dynamic payload type
	if p.Marker {
		second |= 0x80
	}
	buf.WriteByte(second)
	var seq [2]byte
	binary.BigEndian.PutUint16(seq[:], p.SequenceNumber)
	buf.Write(seq[:])
	var ts [4]byte
	binary.BigEndian.PutUint32(ts[:], p.Timestamp)
	buf.Write(ts[:])
	var ssrc [4]byte
	binary.BigEndian.PutUint32(ssrc[:], 0xDEADBEEF)
	buf.Write(ssrc[:])
	buf.Write(p.Payload)
	return buf.Bytes(), nil
}

// DefaultVideoSegments returns the canonical 12-fragment video fixture.
func DefaultVideoSegments() []FragmentSpec {
	specs := make([]FragmentSpec, 0, 12)
	for i := 0; i < 12; i++ {
		specs = append(specs, FragmentSpec{
			SequenceNumber: uint32(i + 1),
			TrackID:        1,
			BaseDecodeTime: uint64(i) * 90000,
			SampleCount:    3,
			SampleDuration: 30000,
			SampleSize:     256,
			PayloadSeed:    0x5000,
		})
	}
	return specs
}

// NanosPerMS is a convenience constant for tests.
const NanosPerMS int64 = model.NanosPerSecond / 1000

// TopLevelOffsets returns the byte offset of every top-level box in data.
// It is used by tests to target a specific fragment for corruption.
func TopLevelOffsets(data []byte) []int64 {
	var out []int64
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		hdr := 8
		if size == 1 && pos+16 <= len(data) {
			size64 := binary.BigEndian.Uint64(data[pos+8 : pos+16])
			if size64 > uint64(len(data)) {
				break
			}
			size = int(size64)
			hdr = 16
		} else if size == 0 {
			size = len(data) - pos
		}
		if size < hdr || pos+size > len(data) {
			break
		}
		out = append(out, int64(pos))
		pos += size
	}
	return out
}
