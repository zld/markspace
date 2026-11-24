package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// MXFCrop is a stored-pixel crop carried by a picture descriptor.
type MXFCrop struct {
	Left, Right, Top, Bottom int
}

// MXFColor is a colour declaration carried by a picture descriptor.
type MXFColor struct {
	Primaries, Transfer, Matrix uint16
	FullRange                   bool
}

// MXFClip is one edit interval on the shared timeline, in frames.
type MXFClip struct {
	StartFrame int64
	FrameCount int64
}

// MXFTrackSpec describes one generated MXF video track.
type MXFTrackSpec struct {
	ID            string
	StoredWidth   int
	StoredHeight  int
	DisplayWidth  int
	DisplayHeight int
	RateNum       int32 // frames per second numerator
	RateDen       int32 // frames per second denominator
	FrameCount    int64
	Rotation      int
	Crop          *MXFCrop
	Color         *MXFColor
	Clips         []MXFClip
}

// MXFSpec describes a whole generated MXF file.
type MXFSpec struct {
	Tracks []MXFTrackSpec
	// TruncateBytes removes this many bytes from the tail, cutting the RIP.
	TruncateBytes int
	// CorruptIndex makes the index table point past EOF.
	CorruptIndex bool
}

// UL is a 16 byte Universal Label.
type UL [16]byte

var ulPrefix = [12]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01}

func newUL(kind [4]byte) UL {
	var u UL
	copy(u[:12], ulPrefix[:])
	copy(u[12:], kind[:])
	return u
}

// Registry keys for the synthetic metadata classes.
var (
	ULHeaderPartition = newUL([4]byte{0x02, 0x05, 0x01, 0x00})
	ULIndexPartition  = newUL([4]byte{0x02, 0x05, 0x01, 0x01})
	ULBodyPartition   = newUL([4]byte{0x02, 0x05, 0x01, 0x02})
	ULIndexTable      = newUL([4]byte{0x04, 0x0e, 0x01, 0x00})
	ULPreface         = newUL([4]byte{0x2f, 0x05, 0x01, 0x00})
	ULContentStorage  = newUL([4]byte{0x23, 0x01, 0x01, 0x00})
	ULMaterialPackage = newUL([4]byte{0x36, 0x01, 0x01, 0x00})
	ULTimeline        = newUL([4]byte{0x36, 0x02, 0x01, 0x00})
	ULVideoTrack      = newUL([4]byte{0x36, 0x03, 0x01, 0x00})
	ULSequence        = newUL([4]byte{0x36, 0x04, 0x01, 0x00})
	ULSourceClip      = newUL([4]byte{0x36, 0x05, 0x01, 0x00})
	ULPictureDesc     = newUL([4]byte{0x28, 0x01, 0x01, 0x00})
	ULRIP             = newUL([4]byte{0x04, 0x0a, 0x01, 0x00})
	ULEssence         = newUL([4]byte{0x15, 0x01, 0x01, 0x00})
)

// Local-set item tags used by the synthetic metadata.
const (
	TagDuration    = 0x0202
	TagStartPos    = 0x1201
	TagTrackID     = 0x4801
	TagEditRate    = 0x4b01
	TagSequenceRef = 0x4803
	TagContentRef  = 0x1901
	TagStoredW     = 0x3201
	TagStoredH     = 0x3202
	TagDisplayW    = 0x3203
	TagDisplayH    = 0x3204
	TagRotation    = 0x3210
	TagCrop        = 0x3211
	TagColor       = 0x3212
	TagFrameCount  = 0x3001
	TagClipStart   = 0x3002
	TagClipDur     = 0x3003
)

// BERLength encodes a short or long-form BER length.
func BERLength(n int) []byte {
	if n < 128 {
		return []byte{byte(n)}
	}
	var tmp [8]byte
	i := 8
	for v := n; v > 0; v >>= 8 {
		i--
		tmp[i] = byte(v)
	}
	return append([]byte{0x80 | byte(8-i)}, tmp[i:]...)
}

// ParseBERLength reads a BER length and returns it with the bytes consumed.
func ParseBERLength(b []byte) (int, int, error) {
	if len(b) == 0 {
		return 0, 0, fmt.Errorf("empty BER length")
	}
	if b[0] < 0x80 {
		return int(b[0]), 1, nil
	}
	n := int(b[0] & 0x7f)
	if n == 0 || n > 8 || len(b) < 1+n {
		return 0, 0, fmt.Errorf("malformed BER length")
	}
	v := 0
	for i := 0; i < n; i++ {
		v = v<<8 | int(b[1+i])
	}
	return v, 1 + n, nil
}

type item struct {
	tag   uint16
	value []byte
}

func localSetBody(items []item) []byte {
	var body []byte
	for _, it := range items {
		body = append(body, byte(it.tag>>8), byte(it.tag))
		body = append(body, BERLength(len(it.value))...)
		body = append(body, it.value...)
	}
	return append(body, 0x00, 0x00)
}

func klv(ul UL, value []byte) []byte {
	out := append([]byte{}, ul[:]...)
	out = append(out, BERLength(len(value))...)
	return append(out, value...)
}

func partitionPack(kind int64, headerByteCount, indexByteCount int64) []byte {
	pack := make([]byte, 88)
	binary.BigEndian.PutUint16(pack[0:2], 0x0002)
	binary.BigEndian.PutUint16(pack[2:4], 0x0002)
	binary.BigEndian.PutUint32(pack[4:8], uint32(kind))
	binary.BigEndian.PutUint64(pack[32:40], uint64(headerByteCount))
	binary.BigEndian.PutUint64(pack[40:48], uint64(indexByteCount))
	binary.BigEndian.PutUint32(pack[84:88], 1)
	return pack
}

func rateBytes(num, den int32) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out[0:4], uint32(num))
	binary.BigEndian.PutUint32(out[4:8], uint32(den))
	return out
}

func be8(v int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(v))
	return b
}

func (spec MXFTrackSpec) sets() [][]byte {
	// Timeline: one sequence of source clips.
	var clipValue []byte
	for _, c := range spec.Clips {
		clipValue = append(clipValue, localSetBody([]item{
			{tag: TagClipStart, value: be8(c.StartFrame)},
			{tag: TagClipDur, value: be8(c.FrameCount)},
		})...)
	}
	sequence := klv(ULSequence, localSetBody([]item{
		{tag: TagDuration, value: be8(spec.FrameCount)},
		{tag: TagSequenceRef, value: clipValue},
	}))
	track := klv(ULVideoTrack, localSetBody([]item{
		{tag: TagTrackID, value: []byte(spec.ID)},
		{tag: TagDuration, value: be8(spec.FrameCount)},
		{tag: TagEditRate, value: rateBytes(spec.RateNum, spec.RateDen)},
	}))
	timeline := klv(ULTimeline, localSetBody([]item{
		{tag: TagDuration, value: be8(spec.FrameCount)},
		{tag: TagEditRate, value: rateBytes(spec.RateNum, spec.RateDen)},
	}))

	desc := []item{
		{tag: TagStoredW, value: be8(int64(spec.StoredWidth))},
		{tag: TagStoredH, value: be8(int64(spec.StoredHeight))},
		{tag: TagDisplayW, value: be8(int64(spec.DisplayWidth))},
		{tag: TagDisplayH, value: be8(int64(spec.DisplayHeight))},
		{tag: TagRotation, value: []byte{byte(spec.Rotation / 90)}},
		{tag: TagFrameCount, value: be8(spec.FrameCount)},
	}
	if spec.Crop != nil {
		desc = append(desc, item{tag: TagCrop, value: []byte{
			byte(spec.Crop.Left >> 8), byte(spec.Crop.Left),
			byte(spec.Crop.Right >> 8), byte(spec.Crop.Right),
			byte(spec.Crop.Top >> 8), byte(spec.Crop.Top),
			byte(spec.Crop.Bottom >> 8), byte(spec.Crop.Bottom),
		}})
	}
	if spec.Color != nil {
		c := spec.Color
		var fr byte
		if c.FullRange {
			fr = 1
		}
		desc = append(desc, item{tag: TagColor, value: []byte{
			byte(c.Primaries >> 8), byte(c.Primaries),
			byte(c.Transfer >> 8), byte(c.Transfer),
			byte(c.Matrix >> 8), byte(c.Matrix), fr,
		}})
	}
	picture := klv(ULPictureDesc, localSetBody(desc))
	return [][]byte{track, timeline, sequence, picture}
}

// BuildMXF renders the spec into a complete MXF byte stream.
func BuildMXF(spec MXFSpec) []byte {
	var headerBody []byte
	for _, t := range spec.Tracks {
		for _, set := range t.sets() {
			headerBody = append(headerBody, set...)
		}
	}
	preface := klv(ULPreface, localSetBody([]item{
		{tag: TagContentRef, value: []byte{0x01, 0x02, 0x03, 0x04}},
	}))
	material := klv(ULMaterialPackage, localSetBody([]item{
		{tag: TagTrackID, value: []byte("material")},
	}))
	headerBody = append(append(preface, material...), headerBody...)

	header := append(klv(ULHeaderPartition, partitionPack(0x02, 0, 0)), headerBody...)
	indexPack := klv(ULIndexPartition, partitionPack(0x03, 0, 0))
	bodyOffset := int64(len(header) + len(indexPack) + 88 + 16 + 32)
	indexValue := be8(bodyOffset)
	if spec.CorruptIndex {
		indexValue = be8(bodyOffset << 40)
	}
	index := append(indexPack, klv(ULIndexTable, indexValue)...)

	body := append(klv(ULBodyPartition, partitionPack(0x04, 0, 0)), klv(ULEssence, bytes.Repeat([]byte{0xbb}, 128))...)

	var file []byte
	file = append(file, []byte("run-in!!")...) // 8 byte run-in
	file = append(file, header...)
	file = append(file, index...)
	file = append(file, body...)
	file = append(file, klv(ULRIP, []byte{0, 0, 0, 0})...)
	if spec.TruncateBytes > 0 {
		if spec.TruncateBytes >= len(file) {
			panic(fmt.Sprintf("fixture: truncate %d exceeds file size %d", spec.TruncateBytes, len(file)))
		}
		file = file[:len(file)-spec.TruncateBytes]
	}
	return file
}

// DefaultMXFSpec returns a single-track 25 fps fixture with one clip.
func DefaultMXFSpec() MXFSpec {
	return MXFSpec{Tracks: []MXFTrackSpec{{
		ID: "mxf-cam", StoredWidth: 1920, StoredHeight: 1080,
		DisplayWidth: 1920, DisplayHeight: 1080,
		RateNum: 25, RateDen: 1, FrameCount: 50,
		Rotation: 0,
		Clips:    []MXFClip{{StartFrame: 0, FrameCount: 50}},
	}}}
}
