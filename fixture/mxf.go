package fixture

import (
	"fmt"
	"hash/crc32"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/internal/mxfschema"
)

// ul is a local alias for the shared schema's universal label type.
type ul = mxfschema.UL

// berLength encodes an MXF BER length (short form or 4-byte long form).
func berLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	return []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func klv(key ul, value []byte) []byte {
	out := make([]byte, 0, 16+5+len(value))
	out = append(out, key[:]...)
	out = append(out, berLength(len(value))...)
	out = append(out, value...)
	return out
}

// localSet serializes a run of tag/ber-length/value entries.
type localSet struct{ buf []byte }

func (s *localSet) u8(tag uint16, v uint8)   { s.put(tag, []byte{v}) }
func (s *localSet) u16(tag uint16, v uint16) { s.put(tag, []byte{byte(v >> 8), byte(v)}) }
func (s *localSet) u32(tag uint16, v uint32) { s.put(tag, u32(v)) }
func (s *localSet) u64(tag uint16, v uint64) { s.put(tag, u64(v)) }
func (s *localSet) str(tag uint16, v string) { s.put(tag, []byte(v)) }
func (s *localSet) bool(tag uint16, v bool) {
	if v {
		s.u8(tag, 1)
		return
	}
	s.u8(tag, 0)
}

func (s *localSet) put(tag uint16, value []byte) {
	s.buf = append(s.buf, byte(tag>>8), byte(tag))
	s.buf = append(s.buf, berLength(len(value))...)
	s.buf = append(s.buf, value...)
}

// partitionPack builds a closed, complete header partition pack body.
func partitionPack(totalSize int64) []byte {
	var s localSet
	s.u16(mxfschema.TagMajorVersion, 1) // major version
	s.u16(mxfschema.TagMinorVersion, 2) // minor version
	s.u32(mxfschema.TagKAGSize, 1)      // KAG size
	s.u64(mxfschema.TagPartitionSize, uint64(totalSize))
	s.u64(mxfschema.TagThisPartition, 0)  // this partition offset
	s.u32(mxfschema.TagPrevPartition, 0)  // previous partition number
	s.u32(mxfschema.TagThisNumber, 1)     // this partition number
	s.u64(mxfschema.TagBodyOffset, 0)     // body offset
	s.u64(mxfschema.TagBodyBodyOffset, 0) // body body offset
	return s.buf
}

func descriptorSet(video bool, priority int) []byte {
	var s localSet
	s.u8(mxfschema.TagSetVersion, 1)
	s.u8(0x0101, uint8(priority))
	if video {
		s.u32(mxfschema.TagTrackID, VideoTrackID)
		s.str(mxfschema.TagTrackKind, "video")
		s.u32(mxfschema.TagTimescale, VideoTimescale)
		s.u64(mxfschema.TagDuration, VideoFrames)
		s.u32(mxfschema.TagWidth, VideoWidth)
		s.u32(mxfschema.TagHeight, VideoHeight)
		if priority == 0 {
			s.u8(mxfschema.TagRotation, VideoRotation)
			s.u32(mxfschema.TagCropWidth, VideoWidth)
			s.u32(mxfschema.TagCropHeight, VideoHeight)
			s.u32(mxfschema.TagCropX, 0)
			s.u32(mxfschema.TagCropY, 0)
			s.u16(mxfschema.TagPrimaries, 1)
			s.u16(mxfschema.TagTransfer, 1)
			s.u16(mxfschema.TagMatrix, 1)
			s.bool(mxfschema.TagFullRange, true)
		} else {
			// Same rotation, disagreeing crop and colour.
			s.u8(mxfschema.TagRotation, VideoRotation)
			s.u32(mxfschema.TagCropWidth, 1904)
			s.u32(mxfschema.TagCropHeight, 1072)
			s.u32(mxfschema.TagCropX, 8)
			s.u32(mxfschema.TagCropY, 4)
			s.u16(mxfschema.TagPrimaries, 9)
			s.u16(mxfschema.TagTransfer, 16)
			s.u16(mxfschema.TagMatrix, 9)
			s.bool(mxfschema.TagFullRange, false)
		}
	} else {
		s.u32(mxfschema.TagTrackID, AudioTrackID)
		s.str(mxfschema.TagTrackKind, "audio")
		s.u32(mxfschema.TagTimescale, AudioRate)
		s.u64(mxfschema.TagDuration, AudioFrames)
		s.u32(mxfschema.TagAudioRate, AudioRate)
		s.u16(mxfschema.TagAudioChan, AudioChannels)
		s.str(mxfschema.TagAudioLayout, AudioLayout)
	}
	return s.buf
}

// buildMXF assembles header partition, preface, descriptors, index and essence.
// Offsets in the index are absolute file offsets of the essence KLV values.
func buildMXF(s *spec) ([]byte, []Block, error) {
	// Essence first, with relative value offsets.
	var essence []byte
	rel := make([]int64, AudioBlocks)
	frame := make([]byte, VideoFrameSize)
	for i := 0; i < VideoFrames; i++ {
		synthVideoFrame(frame, i)
		essence = append(essence, klv(mxfschema.VideoEssence, frame)...)
	}
	blocks := make([]Block, 0, AudioBlocks)
	block := make([]byte, audioBlockBytes)
	for i := 1; i <= AudioBlocks; i++ {
		start := int64(i-1) * AudioBlockFrame
		synthAudioBlock(block, start)
		crc := crc32.ChecksumIEEE(block)
		if i == s.corruptBlock {
			for j := 100; j < 116; j++ {
				block[j] ^= 0xff
			}
		}
		// A 24-byte slice key follows the 16-byte UL in MXF essence KLVs; this
		// profile keeps only UL + BER length + value for clarity.
		rel[i-1] = int64(len(essence)) + 16 + 5
		essence = append(essence, klv(mxfschema.AudioEssence, block)...)
		blocks = append(blocks, Block{
			Index: i, TrackID: AudioTrackID, StartFrame: start,
			Frames: AudioBlockFrame, Length: audioBlockBytes, CRC32: crc,
		})
	}

	preface := klv(mxfschema.Preface, (&localSet{}).buf)
	descV := klv(mxfschema.Descriptor, descriptorSet(true, 0))
	descV2 := klv(mxfschema.Descriptor, descriptorSet(true, 1))
	descA := klv(mxfschema.Descriptor, descriptorSet(false, 0))
	index := func(offsets []int64) []byte {
		var idx localSet
		idx.u8(mxfschema.TagSetVersion, 1)
		idx.u32(mxfschema.TagBlockCount, uint32(len(blocks)))
		for i, b := range blocks {
			var e localSet
			e.u32(mxfschema.TagBlockNo, uint32(b.Index))
			e.u32(mxfschema.TagTrackID, b.TrackID)
			e.u64(mxfschema.TagStartFrame, uint64(b.StartFrame))
			e.u64(mxfschema.TagFrames, uint64(b.Frames))
			e.u64(mxfschema.TagPayloadOff, uint64(offsets[i]))
			e.u32(mxfschema.TagPayloadLen, uint32(b.Length))
			e.u32(mxfschema.TagPayloadCRC, b.CRC32)
			idx.put(mxfschema.TagBlock, e.buf)
		}
		return idx.buf
	}
	// Two passes: size the prefix with placeholder offsets first, then rewrite
	// the partition pack with the true total size. The prefix length is
	// invariant between passes because the pack uses fixed-width fields and
	// every KLV length stays below the 4-byte long-form threshold.
	placeholder := make([]int64, len(blocks))
	prefix := func(offs []int64, total int64) []byte {
		var out []byte
		out = append(out, klv(mxfschema.HeaderPartition, partitionPack(total))...)
		out = append(out, preface...)
		out = append(out, descV...)
		out = append(out, descV2...)
		out = append(out, descA...)
		out = append(out, klv(mxfschema.Index, index(offs))...)
		return out
	}
	pre := prefix(placeholder, 0)
	base := int64(len(pre))
	offsets := make([]int64, len(rel))
	for i, r := range rel {
		offsets[i] = base + r
	}
	final := prefix(offsets, base+int64(len(essence)))
	if int64(len(final)) != base {
		return nil, nil, merr.New(merr.StageFixture, merr.KindInconsistent, 0, 0,
			"mxf prefix size changed between passes (%d -> %d)", base, len(final))
	}
	pre = final
	for i := range blocks {
		blocks[i].Offset = offsets[i]
		if blocks[i].Offset+int64(blocks[i].Length) > base+int64(len(essence)) {
			return nil, nil, merr.New(merr.StageFixture, merr.KindInconsistent, blocks[i].TrackID,
				blocks[i].Offset, "mxf block %d falls outside essence", i+1)
		}
	}
	out := make([]byte, 0, len(pre)+len(essence))
	out = append(out, pre...)
	out = append(out, essence...)
	if len(out) > 0xffffffff {
		return nil, nil, fmt.Errorf("fixture: mxf exceeds 4GiB")
	}
	return out, blocks, nil
}
