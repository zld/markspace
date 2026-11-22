package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"time"

	"example.com/mediatimeline/internal/model"
)

// TrackSpec describes one synthetic track for the writer.
type TrackSpec struct {
	TrackID   model.TrackID
	Timescale uint32
	Handler   string
	SampleDur uint32
	Count     int
	SyncEvery int
	// StartTicks is reserved for future edit-list fixtures.
	StartTicks int64
	// Color is written as an nclx colr box when non-zero.
	Color *model.ColorDeclaration
	// Audio is written into an mp4a sample entry when non-zero.
	Audio *model.AudioDescriptor
	// Payload returns the bytes of sample i; defaults to a deterministic
	// pseudo-frame derived from the track ID and sample number.
	Payload func(i int) []byte
	// DropPayloadAt models a damaged fragment by removing sample bytes while
	// keeping the sample table entry.
	DropPayloadAt map[int]bool
}

// box is a size-prefixed ISO BMFF box under construction.
type box struct {
	typ     string
	payload []byte
}

func newBox(typ string) *box { return &box{typ: typ} }

func (b *box) bytes() []byte {
	out := make([]byte, 8, 8+len(b.payload))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(b.payload)))
	copy(out[4:8], b.typ)
	out = append(out, b.payload...)
	return out[:8+len(b.payload)]
}

func (b *box) add(children ...*box) *box {
	for _, c := range children {
		b.payload = append(b.payload, c.bytes()...)
	}
	return b
}

// child appends one box only if it is non-nil.
func (b *box) child(c *box) *box {
	if c != nil {
		b.payload = append(b.payload, c.bytes()...)
	}
	return b
}

func (b *box) raw(p []byte) *box {
	b.payload = append(b.payload, p...)
	return b
}

// BuildMP4 writes a plain (non-fragmented) MP4 carrying the tracks in specs.
// All samples of a track are stored in one chunk at the start of mdat. The
// moov is built twice: once to learn its size, then with real chunk offsets.
func BuildMP4(specs []TrackSpec) ([]byte, error) {
	ftyp := newBox("ftyp").raw(append([]byte("mp41"), append(u32(1), []byte("iso4")...)...))

	var media bytes.Buffer
	chunkOffsets := make([]uint32, len(specs))
	sampleSizes := make([][]uint32, len(specs))
	for i, spec := range specs {
		chunkOffsets[i] = uint32(media.Len())
		sizes := make([]uint32, spec.Count)
		for n := 0; n < spec.Count; n++ {
			p := sampleBytes(spec, n)
			sizes[n] = uint32(len(p))
			media.Write(p)
		}
		sampleSizes[i] = sizes
	}

	sizeProbe := buildMoov(specs, sampleSizes, chunkOffsets).bytes()
	mdatPayloadStart := len(ftyp.bytes()) + len(sizeProbe) + 8
	finalOffsets := make([]uint32, len(chunkOffsets))
	for i, off := range chunkOffsets {
		finalOffsets[i] = uint32(mdatPayloadStart) + off
	}
	moovBytes := buildMoov(specs, sampleSizes, finalOffsets).bytes()
	mdat := newBox("mdat").raw(media.Bytes())

	out := make([]byte, 0, len(ftyp.bytes())+len(moovBytes)+len(mdat.bytes()))
	out = append(out, ftyp.bytes()...)
	out = append(out, moovBytes...)
	out = append(out, mdat.bytes()...)
	return out, nil
}

func u32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func u16(v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return b[:]
}

func fullHeader(version uint8, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func matrix() []byte {
	vals := []int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	out := make([]byte, 0, 36)
	for _, v := range vals {
		out = append(out, u32(uint32(v))...)
	}
	return out
}

func buildMoov(specs []TrackSpec, sizes [][]uint32, offsets []uint32) *box {
	var maxDur uint64
	for _, s := range specs {
		if d := uint64(s.SampleDur) * uint64(s.Count); d > maxDur {
			maxDur = d
		}
	}
	mvhd := newBox("mvhd").
		raw(fullHeader(0, 0)).
		raw(u32(0)).raw(u32(0)). // creation, modification
		raw(u32(1000)).raw(u32(uint32(maxDur))).
		raw(u32(0x00010000)).raw(u16(0x0100)).raw(u16(0)).
		raw(make([]byte, 8)).
		raw(matrix()).raw(make([]byte, 24)).
		raw(u32(uint32(len(specs) + 1)))
	moov := newBox("moov").add(mvhd)
	for i, spec := range specs {
		moov.add(buildTrak(uint32(i+1), spec, sizes[i], offsets[i]))
	}
	return moov
}

func buildTrak(id uint32, spec TrackSpec, sizes []uint32, offset uint32) *box {
	dur := spec.SampleDur * uint32(spec.Count)
	tkhd := newBox("tkhd").
		raw(fullHeader(0, 0x000007)).
		raw(u32(0)).raw(u32(0)).raw(u32(id)).raw(u32(0)).raw(u32(dur)).
		raw(make([]byte, 8)).
		raw(u16(0)).raw(u16(0)).
		raw(u16(0x0100)).raw(u16(0)).
		raw(matrix()).
		raw(u32(1920 << 16)).raw(u32(1080 << 16))

	mdhd := newBox("mdhd").
		raw(fullHeader(0, 0)).
		raw(u32(0)).raw(u32(0)).
		raw(u32(spec.Timescale)).raw(u32(dur)).
		raw(u16(0x55c4)).raw(u16(0)) // language "und"

	hdlr := newBox("hdlr").
		raw(fullHeader(0, 0)).
		raw(u32(0)).
		raw([]byte(spec.Handler)).
		raw(make([]byte, 12)).
		raw([]byte("VideoHandler\x00"))

	var mh *box
	if spec.Handler == "soun" {
		mh = newBox("smhd").raw(fullHeader(0, 0)).raw(u16(0)).raw(u16(0))
	} else {
		mh = newBox("vmhd").raw(fullHeader(0, 1)).raw(u16(0)).raw(u16(0)).raw(u16(0)).raw(u16(0))
	}

	dinf := newBox("dinf").add(newBox("dref").raw(fullHeader(0, 0)).raw(u32(1)).
		add(newBox("url ").raw(fullHeader(0, 1))))

	stbl := newBox("stbl").
		add(buildStsd(spec)).
		add(buildStts(spec)).
		add(buildStsz(sizes)).
		add(newBox("stsc").raw(fullHeader(0, 0)).raw(u32(1)).raw(u32(1)).raw(u32(uint32(spec.Count))).raw(u32(1))).
		add(newBox("stco").raw(fullHeader(0, 0)).raw(u32(1)).raw(u32(offset)))
	if spec.SyncEvery > 0 {
		var nums []uint32
		for i := 0; i < spec.Count; i += spec.SyncEvery {
			nums = append(nums, uint32(i+1))
		}
		stss := newBox("stss").raw(fullHeader(0, 0)).raw(u32(uint32(len(nums))))
		for _, n := range nums {
			stss.raw(u32(n))
		}
		stbl.child(stss)
	}

	minf := newBox("minf").add(mh, dinf, stbl)
	mdia := newBox("mdia").add(mdhd, hdlr, minf)
	return newBox("trak").add(tkhd, mdia)
}

func buildStsd(spec TrackSpec) *box {
	var entry *box
	if spec.Handler == "soun" {
		channels := uint16(2)
		rate := uint32(48000)
		if spec.Audio != nil {
			channels = uint16(spec.Audio.ChannelCount)
			rate = spec.Audio.SampleRate
		}
		entry = newBox("mp4a").
			raw(make([]byte, 6)).raw(u16(1)). // reserved, data_reference_index
			raw(make([]byte, 8)).
			raw(u16(channels)).raw(u16(16)).raw(u16(0)).raw(u16(0)).
			raw(u32(rate << 16))
	} else {
		entry = newBox("avc1").
			raw(make([]byte, 6)).raw(u16(1)).
			raw(make([]byte, 16)).
			raw(u16(1920)).raw(u16(1080)).
			raw(u32(0x00480000)).raw(u32(0x00480000)).
			raw(u32(0)).raw(u16(1)).
			raw(make([]byte, 32)).
			raw(u16(0x0018)).raw(u16(0xffff))
		if spec.Color != nil {
			colr := newBox("colr").
				raw([]byte("nclx")).
				raw(u16(spec.Color.Primaries)).
				raw(u16(spec.Color.Transfer)).
				raw(u16(spec.Color.Matrix))
			if spec.Color.FullRange {
				colr.raw([]byte{0x80})
			} else {
				colr.raw([]byte{0x00})
			}
			entry.child(colr)
		}
	}
	return newBox("stsd").raw(fullHeader(0, 0)).raw(u32(1)).add(entry)
}

func buildStts(spec TrackSpec) *box {
	return newBox("stts").raw(fullHeader(0, 0)).raw(u32(1)).
		raw(u32(uint32(spec.Count))).raw(u32(spec.SampleDur))
}

func buildStsz(sizes []uint32) *box {
	b := newBox("stsz").raw(fullHeader(0, 0)).raw(u32(0)).raw(u32(uint32(len(sizes))))
	for _, s := range sizes {
		b.raw(u32(s))
	}
	return b
}

func sampleBytes(spec TrackSpec, i int) []byte {
	if spec.DropPayloadAt[i] {
		return nil
	}
	if spec.Payload != nil {
		return spec.Payload(i)
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", spec.TrackID, i)))
	return h[:]
}

// BuildEmsg builds an emsg v1 payload (without the box header).
func BuildEmsg(seconds float64, scheme, value string, timescale uint32, id uint32, msg []byte) []byte {
	var body bytes.Buffer
	body.WriteByte(1)
	body.Write([]byte{0, 0, 0})
	binary.Write(&body, binary.BigEndian, timescale)
	binary.Write(&body, binary.BigEndian, uint64(seconds*float64(timescale)))
	binary.Write(&body, binary.BigEndian, uint32(1))
	binary.Write(&body, binary.BigEndian, id)
	body.WriteString(scheme)
	body.WriteByte(0)
	body.WriteString(value)
	body.WriteByte(0)
	body.Write(msg)
	return body.Bytes()
}

// BuildID3 builds an ID3v2.3 tag with TIT2 and TXXX frames.
func BuildID3(title, key, value string) []byte {
	var frames bytes.Buffer
	writeFrame := func(id string, text string) {
		var data bytes.Buffer
		data.WriteByte(3) // UTF-8
		data.WriteString(text)
		frames.WriteString(id)
		binary.Write(&frames, binary.BigEndian, uint32(data.Len()))
		frames.Write([]byte{0, 0})
		frames.Write(data.Bytes())
	}
	writeFrame("TIT2", title)
	writeFrame("TXXX", key+"\x00"+value)
	var tag bytes.Buffer
	tag.WriteString("ID3")
	tag.Write([]byte{3, 0, 0})
	size := frames.Len()
	tag.Write([]byte{byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)})
	tag.Write(frames.Bytes())
	return tag.Bytes()
}

// BuildWebVTT renders cues as a WebVTT document.
func BuildWebVTT(cues []Cue) []byte {
	var b bytes.Buffer
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		fmt.Fprintf(&b, "cue-%02d\n%s --> %s\n%s\n\n", i, vttStamp(c.StartMS), vttStamp(c.EndMS), c.Text)
	}
	return b.Bytes()
}

// Cue is a fixture subtitle cue.
type Cue struct {
	StartMS int64
	EndMS   int64
	Text    string
}

func vttStamp(ms int64) string {
	h := ms / 3600000
	m := ms / 60000 % 60
	s := ms / 1000 % 60
	r := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, r)
}

// BuildChapters renders a chapter list.
func BuildChapters(chapters []Chapter) []byte {
	var b bytes.Buffer
	for i, c := range chapters {
		fmt.Fprintf(&b, "CHAPTER%02d=%s\nCHAPTER%02dNAME=%s\n", i+1, vttStamp(c.StartMS), i+1, c.Title)
	}
	return b.Bytes()
}

// Chapter is a fixture chapter mark.
type Chapter struct {
	StartMS int64
	Title   string
}

// NewRand returns a deterministic PRNG for a seed.
func NewRand(seed int64) *rand.Rand {
	return rand.New(rand.NewSource(seed))
}

// Now is the fixed wall clock the generator reports, so output is stable.
var Now = time.Unix(1700000000, 0).UTC()
