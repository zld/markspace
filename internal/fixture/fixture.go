// Package fixture generates self-contained, deterministic media inputs for
// tests and demos: multi-track fragmented-style MP4 files with emsg boxes
// and length-prefixed RTP/ID3 captures. No external files are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math/rand"
)

// MP4TrackSpec describes one generated MP4 track.
type MP4TrackSpec struct {
	TrackID     uint32
	Timescale   uint32
	Samples     int
	Delta       uint32 // per-sample duration in track timescale
	KeyEvery    int    // >0: every nth sample is a sync sample
	PayloadBase byte
}

// MP4Input is a generated MP4 byte stream plus the ground truth used by
// tests to validate the parser.
type MP4Input struct {
	Bytes     []byte
	Tracks    []MP4TrackSpec
	EmsgCount int
	// EmsgMessage overrides the emsg message payload; empty means default.
	EmsgMessage string
	// SampleOffset[i] is the mdat payload offset of sample i in track order.
	SampleOffset map[uint32][]int64
}

func boxHeader(buf *bytes.Buffer, typ string, size int) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(size))
	copy(hdr[4:8], []byte(typ))
	buf.Write(hdr)
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	var b bytes.Buffer
	boxHeader(&b, typ, 8+4+len(payload))
	// 1-byte version followed by 3 raw flag bytes (not a big-endian uint32).
	b.WriteByte(version)
	b.WriteByte(byte(flags))
	b.WriteByte(byte(flags >> 8))
	b.WriteByte(byte(flags >> 16))
	b.Write(payload)
	return b.Bytes()
}

// chunkPatch records where inside moov a track's stco absolute offset lives.
type chunkPatch struct {
	trackID uint32
	pos     int // byte position of the uint32 offset within the final file
}

// GenerateMP4 builds a deterministic multi-track MP4 with one emsg box.
func GenerateMP4(seed int64, tracks []MP4TrackSpec) MP4Input {
	return GenerateMP4WithEmsg(seed, tracks, "event-payload", 250)
}

// GenerateMP4WithEmsg builds an MP4 whose emsg message and presentation-time
// delta are caller-controlled, enabling cross-source conflict fixtures.
func GenerateMP4WithEmsg(seed int64, tracks []MP4TrackSpec, emsgMessage string, emsgDelta uint32) MP4Input {
	rng := rand.New(rand.NewSource(seed))
	out := &MP4Input{Tracks: tracks, SampleOffset: map[uint32][]int64{}, EmsgMessage: emsgMessage}

	var ftyp bytes.Buffer
	boxHeader(&ftyp, "ftyp", 8+8)
	ftyp.WriteString("isom")
	ftyp.WriteString("iso5")

	var moov bytes.Buffer
	boxHeader(&moov, "moov", 0) // placeholder size, patched below
	moovStart := moov.Len()

	// mvhd (version 0), timescale 1000.
	var mvhd bytes.Buffer
	mvhd.Write(make([]byte, 8)) // creation/modification
	binary.Write(&mvhd, binary.BigEndian, uint32(1000))
	binary.Write(&mvhd, binary.BigEndian, uint32(0)) // duration
	mvhd.Write(make([]byte, 80))                     // rate..next-track-id
	moov.Write(fullBox("mvhd", 0, 0, mvhd.Bytes()))

	var mdat bytes.Buffer
	boxHeader(&mdat, "mdat", 0) // size patched after payloads

	var patches []chunkPatch
	for _, spec := range tracks {
		out.SampleOffset[spec.TrackID] = []int64{}
		// Offsets returned by writeTrack include the mdat header; normalize
		// them to payload-relative so chunkBase math stays consistent.
		offsets, patch := writeTrack(&moov, &mdat, spec, rng)
		patch.trackID = spec.TrackID
		patches = append(patches, patch)
		for i := range offsets {
			offsets[i] -= 8
		}
		out.SampleOffset[spec.TrackID] = offsets
	}

	// One emsg box (version 0) inside moov for parser coverage.
	var emsg bytes.Buffer
	emsg.WriteString("urn:test:scheme")
	emsg.WriteByte(0)
	emsg.WriteString("v0")
	emsg.WriteByte(0)
	binary.Write(&emsg, binary.BigEndian, uint32(1000)) // timescale
	binary.Write(&emsg, binary.BigEndian, emsgDelta)    // presentation-time delta
	binary.Write(&emsg, binary.BigEndian, uint32(0))    // duration
	binary.Write(&emsg, binary.BigEndian, uint32(7))    // id
	emsg.WriteString(emsgMessage)
	moov.Write(fullBox("emsg", 0, 0, emsg.Bytes()))
	out.EmsgCount = 1

	// Patch moov size.
	moovSize := moov.Len() - moovStart + 8
	binary.BigEndian.PutUint32(moov.Bytes()[moovStart-8:moovStart-4], uint32(moovSize))

	// Resolve absolute sample offsets and stco chunk pointers. Sample
	// offsets are mdat-payload-relative (header excluded); the payload
	// starts right after the 8-byte mdat header.
	chunkBase := int64(ftyp.Len() + moov.Len() + 8)
	for id, rel := range out.SampleOffset {
		for i := range rel {
			rel[i] += chunkBase
		}
		out.SampleOffset[id] = rel
	}
	for _, pc := range patches {
		// pc.pos is relative to moov start; moov begins after ftyp.
		absPos := ftyp.Len() + pc.pos
		binary.BigEndian.PutUint32(moov.Bytes()[pc.pos:pc.pos+4],
			uint32(chunkBase))
		_ = absPos
	}
	// Patch mdat size.
	binary.BigEndian.PutUint32(mdat.Bytes()[0:4], uint32(mdat.Len()))

	var file bytes.Buffer
	file.Write(ftyp.Bytes())
	file.Write(moov.Bytes())
	file.Write(mdat.Bytes())
	out.Bytes = file.Bytes()
	return *out
}

func writeTrack(moov, mdat *bytes.Buffer, spec MP4TrackSpec, rng *rand.Rand) ([]int64, chunkPatch) {
	var trak bytes.Buffer
	boxHeader(&trak, "trak", 0)
	trakStart := trak.Len()

	var tkhd bytes.Buffer
	tkhd.Write(make([]byte, 8))
	binary.Write(&tkhd, binary.BigEndian, spec.TrackID)
	tkhd.Write(make([]byte, 76))
	trak.Write(fullBox("tkhd", 0, 3, tkhd.Bytes()))

	var mdia bytes.Buffer
	boxHeader(&mdia, "mdia", 0)
	mdiaStart := mdia.Len()
	var mdhd bytes.Buffer
	mdhd.Write(make([]byte, 8))
	binary.Write(&mdhd, binary.BigEndian, spec.Timescale)
	binary.Write(&mdhd, binary.BigEndian, uint32(int(spec.Delta)*spec.Samples))
	mdhd.Write(make([]byte, 4)) // language+predefined
	mdia.Write(fullBox("mdhd", 0, 0, mdhd.Bytes()))

	// hdlr
	var hdlr bytes.Buffer
	hdlr.Write(make([]byte, 4))
	hdlr.WriteString("vide")
	hdlr.Write(make([]byte, 12))
	hdlr.WriteString("gen")
	hdlr.WriteByte(0)
	mdia.Write(fullBox("hdlr", 0, 0, hdlr.Bytes()))

	// minf > stbl with sample tables; one chunk containing all samples.
	var stbl bytes.Buffer
	boxHeader(&stbl, "stbl", 0)
	stblStart := stbl.Len()

	sizes := make([]uint32, spec.Samples)
	payloads := make([][]byte, spec.Samples)
	for i := range sizes {
		size := 16 + rng.Intn(16)
		sizes[i] = uint32(size)
		p := make([]byte, size)
		for j := range p {
			p[j] = spec.PayloadBase + byte((i+j)%251)
		}
		payloads[i] = p
	}

	// stts: uniform delta.
	var stts bytes.Buffer
	binary.Write(&stts, binary.BigEndian, uint32(1))
	binary.Write(&stts, binary.BigEndian, uint32(spec.Samples))
	binary.Write(&stts, binary.BigEndian, spec.Delta)
	stbl.Write(fullBox("stts", 0, 0, stts.Bytes()))

	// stss: keyframes.
	if spec.KeyEvery > 0 {
		var keys []uint32
		for i := 1; i <= spec.Samples; i++ {
			if (i-1)%spec.KeyEvery == 0 {
				keys = append(keys, uint32(i))
			}
		}
		var stss bytes.Buffer
		binary.Write(&stss, binary.BigEndian, uint32(len(keys)))
		for _, k := range keys {
			binary.Write(&stss, binary.BigEndian, k)
		}
		stbl.Write(fullBox("stss", 0, 0, stss.Bytes()))
	}

	// stsc: one chunk, all samples.
	var stsc bytes.Buffer
	binary.Write(&stsc, binary.BigEndian, uint32(1))
	binary.Write(&stsc, binary.BigEndian, uint32(1))
	binary.Write(&stsc, binary.BigEndian, uint32(spec.Samples))
	binary.Write(&stsc, binary.BigEndian, uint32(1))
	stbl.Write(fullBox("stsc", 0, 0, stsc.Bytes()))

	// stsz.
	var stsz bytes.Buffer
	binary.Write(&stsz, binary.BigEndian, uint32(0))
	binary.Write(&stsz, binary.BigEndian, uint32(spec.Samples))
	for _, s := range sizes {
		binary.Write(&stsz, binary.BigEndian, s)
	}
	stbl.Write(fullBox("stsz", 0, 0, stsz.Bytes()))

	// stco: build the full box (header included) ourselves so the position
	// of the placeholder chunk offset is exactly known. Total 20 bytes:
	// 8 box header + 4 version/flags + 4 entry count + 4 chunk offset.
	var stcoBox bytes.Buffer
	boxHeader(&stcoBox, "stco", 20)
	stcoBox.Write([]byte{0, 0, 0, 0}) // version + flags
	binary.Write(&stcoBox, binary.BigEndian, uint32(1))
	// Placeholder position relative to moov start. The stbl box is later
	// wrapped by an 8-byte minf container header.
	stcoPlaceholder := moov.Len() + trak.Len() + mdia.Len() + 8 +
		stbl.Len() + 16
	stcoBox.Write(make([]byte, 4))
	stbl.Write(stcoBox.Bytes())

	stblSize := stbl.Len() - stblStart + 8
	binary.BigEndian.PutUint32(stbl.Bytes()[stblStart-8:stblStart-4], uint32(stblSize))

	var minf bytes.Buffer
	boxHeader(&minf, "minf", 0)
	minfStart := minf.Len()
	minf.Write(stbl.Bytes())
	minfSize := minf.Len() - minfStart + 8
	binary.BigEndian.PutUint32(minf.Bytes()[minfStart-8:minfStart-4], uint32(minfSize))
	mdia.Write(minf.Bytes())

	mdiaSize := mdia.Len() - mdiaStart + 8
	binary.BigEndian.PutUint32(mdia.Bytes()[mdiaStart-8:mdiaStart-4], uint32(mdiaSize))
	trak.Write(mdia.Bytes())
	trakSize := trak.Len() - trakStart + 8
	binary.BigEndian.PutUint32(trak.Bytes()[trakStart-8:trakStart-4], uint32(trakSize))
	moov.Write(trak.Bytes())

	// Append payloads to mdat and record relative offsets.
	offsets := make([]int64, spec.Samples)
	for i, p := range payloads {
		offsets[i] = int64(mdat.Len())
		mdat.Write(p)
	}
	return offsets, chunkPatch{pos: stcoPlaceholder}
}

// TruncateAt returns a copy cut at byte offset (for corruption tests).
func TruncateAt(in []byte, offset int) []byte {
	if offset > len(in) {
		offset = len(in)
	}
	out := make([]byte, offset)
	copy(out, in[:offset])
	return out
}
