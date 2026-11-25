package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/abema/go-mp4"
)

// mp4Box is a fully serialized box, header included.
type mp4Box []byte

func rawBox(typ string, payload []byte) mp4Box {
	if len(typ) != 4 {
		panic("fixture: box type must be 4 characters: " + typ)
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func containerBox(typ string, children ...mp4Box) mp4Box {
	var payload []byte
	for _, c := range children {
		payload = append(payload, c...)
	}
	return rawBox(typ, payload)
}

func marshalBox(b mp4.IImmutableBox) mp4Box {
	return rawBox(b.GetType().String(), marshalPayload(b))
}

func marshalPayload(b mp4.IImmutableBox) []byte {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		panic(fmt.Sprintf("fixture: marshal %s: %v", b.GetType().String(), err))
	}
	return buf.Bytes()
}

func fullBox(typ string, version uint8, flags uint32, payload []byte) mp4Box {
	body := make([]byte, 4+len(payload))
	body[0] = version
	body[1] = byte(flags >> 16)
	body[2] = byte(flags >> 8)
	body[3] = byte(flags)
	copy(body[4:], payload)
	return rawBox(typ, body)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// identityMatrix is the unity display matrix in 16.16 fixed point.
var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

// rotationMatrix encodes a clockwise display rotation of deg degrees.
func rotationMatrix(deg int) ([9]int32, error) {
	switch deg {
	case 0:
		return identityMatrix, nil
	case 90:
		return [9]int32{0, 0x00010000, 0, -0x00010000, 0, 0, 0, 0, 0x40000000}, nil
	case 180:
		return [9]int32{-0x00010000, 0, 0, 0, -0x00010000, 0, 0, 0, 0x40000000}, nil
	case 270:
		return [9]int32{0, -0x00010000, 0, 0x00010000, 0, 0, 0, 0, 0x40000000}, nil
	default:
		return [9]int32{}, fmt.Errorf("fixture: unsupported rotation %d", deg)
	}
}

// mp4Essence holds the mdat payload plus the per-block bookkeeping.
type mp4Essence struct {
	payload    []byte
	videoBytes int
	blocks     []Block
	crcs       []uint32
}

// buildMP4 writes ftyp, mdat and moov. Offsets in the stco/bcrc tables are
// computed from the actual serialized ftyp size, so layout is always exact.
func buildMP4(s *spec) ([]byte, []Block, error) {
	ess, err := buildMP4Essence(s)
	if err != nil {
		return nil, nil, err
	}
	ftyp := marshalBox(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	})
	mdatStart := int64(len(ftyp) + 8) // mdat header
	blocks := make([]Block, len(ess.blocks))
	for i, b := range ess.blocks {
		b.Offset += mdatStart
		blocks[i] = b
	}
	mdat := rawBox("mdat", ess.payload)
	moov, err := buildMP4Moov(s, ess, blocks, mdatStart)
	if err != nil {
		return nil, nil, err
	}
	out := make([]byte, 0, len(ftyp)+len(mdat)+len(moov))
	out = append(out, ftyp...)
	out = append(out, mdat...)
	out = append(out, moov...)
	return out, blocks, nil
}

func buildMP4Essence(s *spec) (*mp4Essence, error) {
	ess := &mp4Essence{}
	frame := make([]byte, VideoFrameSize)
	for i := 0; i < VideoFrames; i++ {
		synthVideoFrame(frame, i)
		ess.payload = append(ess.payload, frame...)
	}
	ess.videoBytes = len(ess.payload)
	block := make([]byte, audioBlockBytes)
	for i := 1; i <= AudioBlocks; i++ {
		start := int64(i-1) * AudioBlockFrame
		synthAudioBlock(block, start)
		// The truncated-audio fixture's final block is stored three bytes
		// short, so a reader sees a partial sample frame at the track end. Its
		// checksum describes exactly the stored bytes.
		stored := block
		if s.partialAudio && i == AudioBlocks {
			stored = block[:len(block)-3]
		}
		crc := crc32.ChecksumIEEE(stored)
		if i == s.corruptBlock {
			// Damage essence while the recorded checksum keeps describing the
			// intact block: a half-written publication.
			for j := 100; j < 116; j++ {
				block[j] ^= 0xff
			}
		}
		rel := int64(ess.videoBytes + (i-1)*audioBlockBytes)
		ess.payload = append(ess.payload, stored...)
		ess.blocks = append(ess.blocks, Block{
			Index: i, TrackID: AudioTrackID,
			StartFrame: start, Frames: AudioBlockFrame,
			Offset: rel, Length: len(stored), CRC32: crc,
		})
		ess.crcs = append(ess.crcs, crc)
	}
	return ess, nil
}

func buildMP4Moov(s *spec, ess *mp4Essence, blocks []Block, mdatStart int64) (mp4Box, error) {
	mvhd := marshalBox(&mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  VideoFrames * 1000 / FPS,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix,
		NextTrackID: 3,
	})
	videoTrak, err := buildMP4VideoTrak(mdatStart)
	if err != nil {
		return nil, err
	}
	audioTrak, err := buildMP4AudioTrak(s, ess, blocks)
	if err != nil {
		return nil, err
	}
	return containerBox("moov", mvhd, videoTrak, audioTrak), nil
}

func buildMP4VideoTrak(mdatStart int64) (mp4Box, error) {
	matrix, err := rotationMatrix(VideoRotation)
	if err != nil {
		return nil, err
	}
	tkhd := marshalBox(&mp4.Tkhd{
		TrackID:    VideoTrackID,
		DurationV0: VideoFrames * 1000 / FPS,
		Matrix:     matrix,
		Width:      VideoWidth << 16,
		Height:     VideoHeight << 16,
	})
	mdhd := marshalBox(&mp4.Mdhd{Timescale: VideoTimescale, DurationV0: VideoFrames, Language: [3]byte{'u', 'n', 'd'}})
	hdlr := marshalBox(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler\x00"})
	vmhd := fullBox("vmhd", 0, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	dinf := containerBox("dinf", fullBox("dref", 0, 0, append(u32(1), fullBox("url ", 0, 1, nil)...)))
	stsd, err := buildMP4VideoStsd()
	if err != nil {
		return nil, err
	}
	offsets := make([]uint32, VideoFrames)
	for i := range offsets {
		offsets[i] = uint32(mdatStart) + uint32(i*VideoFrameSize)
	}
	sizes := make([]uint32, VideoFrames)
	for i := range sizes {
		sizes[i] = VideoFrameSize
	}
	stbl := containerBox("stbl",
		stsd,
		marshalBox(&mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: VideoFrames, SampleDelta: 1}}}),
		marshalBox(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}),
		marshalBox(&mp4.Stsz{SampleCount: VideoFrames, EntrySize: sizes}),
		marshalBox(&mp4.Stco{EntryCount: VideoFrames, ChunkOffset: offsets}),
	)
	minf := containerBox("minf", vmhd, dinf, stbl)
	mdia := containerBox("mdia", mdhd, hdlr, minf)
	return containerBox("trak", tkhd, mdia), nil
}
