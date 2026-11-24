package fixture

import (
	"encoding/binary"
	"fmt"

	"archive/internal/model"
)

// BuildMXF renders a spec as an MXF subset: a header partition with a primer
// pack and header metadata (preface, identification, material/source
// packages, timeline tracks, sequences and essence descriptors), a body
// partition whose KLV packets carry the frame essence, and one index table
// segment per indexed track. The set layout is deliberately small but uses
// real SMPTE universal labels for the keys, real BER lengths, and a proper
// partition-pack header so the file is structurally an MXF, not a private
// format wearing MXF key names.
//
// Every frame payload is an essence frame (see package essence), so recovery
// can resynchronise on a frame boundary even when the partition index is
// unusable.
func BuildMXF(s Spec) (*Layout, []byte, error) {
	if s.Format != model.ContainerMXF {
		return nil, nil, fmt.Errorf("fixture: %s is not an mxf spec", s.Name)
	}
	if err := s.Validate(); err != nil {
		return nil, nil, err
	}
	layout := &Layout{Spec: s}
	tracks := make([]*TrackLayout, len(s.Tracks))
	for i, ts := range s.Tracks {
		tracks[i] = &TrackLayout{Spec: ts}
	}
	layout.Tracks = tracks

	instanceUID := [16]byte{0xA5}
	copy(instanceUID[1:], []byte("archive-fixture"))
	bodyUL := [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x04, 0x01, 0x01, 0x01, 0x0D, 0x01, 0x02, 0x01, 0x01, 0x02, 0x00, 0x00}

	var buf []byte

	// Header partition. The pack body length is known before writing because
	// the metadata size is independent of the media offsets.
	headerMeta := buildHeaderMetadata(s, instanceUID)
	primer := buildPrimerPack()
	headerPayload := append(primer, headerMeta...)
	buf = append(buf, buildPartitionPack(keyHeaderMeta, partitionHeader, 0, headerPayload, bodyUL)...)

	// Body partition, patched after essence is laid out.
	bodyStart := len(buf)
	bodyPack := buildPartitionPack(keyBodyPartition, partitionBody, 1, nil, bodyUL)
	buf = append(buf, bodyPack...)
	bodyPayloadStart := uint64(len(buf))

	frames := make([][][]byte, len(tracks))
	for ti, tl := range tracks {
		frames[ti] = make([][]byte, tl.Spec.Frames)
		for i := 0; i < tl.Spec.Frames; i++ {
			frames[ti][i] = BuildFrame(tl.Spec, i)
		}
	}

	cursor := bodyPayloadStart
	for i := 0; ; i++ {
		wrote := false
		for ti, tl := range tracks {
			if i >= len(frames[ti]) {
				continue
			}
			wrote = true
			tl.Frames = append(tl.Frames, FrameLayout{
				Seq:       uint32(i),
				Offset:    cursor,
				Size:      uint64(len(frames[ti][i])),
				StartTick: tl.Spec.StartTick + uint64(i)*tl.Spec.FrameTicks,
				Duration:  tl.Spec.FrameTicks,
				Sync:      tl.Spec.SyncEvery <= 1 || i%tl.Spec.SyncEvery == 0,
			})
			klv := append(klvHeader(essenceKey(tl.Spec.Kind), uint64(len(frames[ti][i]))), frames[ti][i]...)
			buf = append(buf, klv...)
			cursor += uint64(len(klv))
		}
		if !wrote {
			break
		}
	}
	layout.MediaStart = bodyPayloadStart
	layout.MediaEnd = cursor

	// Index segments follow the media so truncating the media area does not
	// silently look like a complete file with a valid index.
	indexStarts := make([]int, len(tracks))
	for ti, tl := range tracks {
		if tl.Spec.DropsIndex {
			continue
		}
		indexStarts[ti] = len(buf)
		buf = append(buf, buildIndexSegment(tl)...)
	}

	footerStart := len(buf)
	buf = append(buf, buildPartitionPack(keyFooter, partitionFooter, 0, nil, bodyUL)...)

	_ = bodyStart
	_ = bodyPack
	_ = footerStart

	layout.Size = uint64(len(buf))
	return layout, buf, nil
}

type partitionPosition int

const (
	partitionHeader partitionPosition = iota
	partitionBody
	partitionFooter
)

// essenceKey returns the essence element key for a track kind.
func essenceKey(k model.TrackKind) [16]byte {
	switch k {
	case model.KindAudio:
		return keyEssenceElementAudio
	case model.KindData:
		return keyEssenceElementData
	default:
		return keyEssenceElementVideo
	}
}

// klvHeader builds a 16-byte key followed by a 4-byte BER length
// (0x83 + 3 bytes), which the probe reads back exactly.
func klvHeader(key [16]byte, size uint64) []byte {
	out := make([]byte, 20)
	copy(out[0:16], key[:])
	out[16] = 0x83
	out[17] = byte(size >> 16)
	out[18] = byte(size >> 8)
	out[19] = byte(size)
	return out
}

// buildPartitionPack writes a 16-byte key, a 4-byte BER length and a fixed
// 76-byte value. payload is emitted after the pack as the partition content.
func buildPartitionPack(key [16]byte, pos partitionPosition, index uint32, payload []byte, bodyUL [16]byte) []byte {
	value := make([]byte, 76)
	binary.BigEndian.PutUint16(value[0:2], 0)      // major version
	binary.BigEndian.PutUint16(value[2:4], 0x0001) // minor version
	binary.BigEndian.PutUint32(value[4:8], index)  // partition index
	copy(value[8:24], keyOperationalPattern[:])    // operational pattern
	copy(value[24:40], bodyUL[:])                  // essence container
	copy(value[40:48], []byte("archive"))          // company name
	copy(value[48:56], []byte("fixture"))          // product name
	value[56] = byte(pos)
	// The remaining bytes stay zero.
	out := klvHeader(key, uint64(len(value)))
	out = append(out, value...)
	return append(out, payload...)
}

func buildPrimerPack() []byte {
	var body []byte
	for i, tag := range primerTags {
		e := make([]byte, 8)
		binary.BigEndian.PutUint16(e[0:2], tag)
		copy(e[2:8], []byte{0x01, 0x02, 0x03, byte(i >> 16), byte(i >> 8), byte(i)})
		body = append(body, e...)
	}
	return append(klvHeader(keyPrimerPack, uint64(len(body))), body...)
}

var primerTags = []uint16{
	tInstanceUID, tGenerationUID, tPackageUID, tPackageName, tTrackID,
	tTrackNumber, tEditRate, tSegmentDuration, tDuration, tDataDefinition,
	tSampleRate, tContainerDuration, tEssenceContainer, tCodec,
	tEssenceContainers, tOperationalPattern, tRevNum, tModNum,
	tColorPrimaries, tTransferCharacteristic, tCodingEquations,
}

func buildHeaderMetadata(s Spec, instanceUID [16]byte) []byte {
	var body []byte
	add := func(set []byte) { body = append(body, set...) }

	preface := &localSet{key: keyPrefface}
	preface.u16(tRevNum, 2)
	preface.u16(tModNum, 3)
	preface.uid(tInstanceUID, instanceUID)
	preface.uid(tGenerationUID, [16]byte{0x01})
	preface.u16(tOperationalPattern, 0x0101)
	preface.u16(tEssenceContainers, 0x0102)
	add(preface.build())

	ident := &localSet{key: keyIdent}
	ident.u16(tRevNum, 1)
	ident.u16(tProductVersion, 2)
	ident.text(tCompanyName, "archive")
	ident.text(tProductName, "fixture")
	ident.text(tVersionString, "1.0")
	ident.text(tPlatform, "darwin")
	add(ident.build())

	mpkg := &localSet{key: keyMaterialPkg}
	mpkg.uid(tPackageUID, [16]byte{0x10, 0x01})
	mpkg.text(tPackageName, s.Name)
	add(mpkg.build())

	for _, ts := range s.Tracks {
		spkg := &localSet{key: keySourcePkg}
		spkg.uid(tPackageUID, [16]byte{0x20, byte(ts.TrackID)})
		spkg.text(tPackageName, ts.Logical)
		add(spkg.build())

		// Timeline tracks are emitted per source package, identified by a
		// SyntheticTrackID equal to the fixture track id. The probe matches
		// them to descriptors through this ID.
		tt := &localSet{key: keyTimelineTrack}
		tt.u32(tTrackID, ts.TrackID)
		tt.u16(tTrackNumber, uint16(ts.TrackID))
		tt.u32(tEditRate, ts.Timescale)
		tt.uid(tInstanceUID, [16]byte{0x30, byte(ts.TrackID)})
		add(tt.build())

		seq := &localSet{key: keySequence}
		seq.u32(tDuration, uint32(ts.Frames)*uint32(ts.FrameTicks))
		seq.u32(tEditRate, ts.Timescale)
		add(seq.build())

		desc := &localSet{key: descriptorKey(ts.Kind)}
		// LinkedTrackID ties the descriptor to the timeline track number,
		// which is how the probe associates metadata with essence.
		desc.u32(tLinkedTrackID, ts.TrackID)
		desc.u32(tSampleRate, ts.Timescale)
		desc.u32(tContainerDuration, uint32(ts.Frames)*uint32(ts.FrameTicks))
		desc.u16(tEssenceContainer, 0x0102)
		desc.text(tCodec, ts.Codec)
		switch ts.Kind {
		case model.KindVideo:
			w, h := 1920, 1080
			if ts.Crop != nil {
				w, h = ts.Crop.Right-ts.Crop.Left, ts.Crop.Bottom-ts.Crop.Top
			}
			desc.u32(tStoredWidth, uint32(w))
			desc.u32(tStoredHeight, uint32(h))
			desc.u32(tDisplayWidth, uint32(w))
			desc.u32(tDisplayHeight, uint32(h))
			desc.u8(tFrameLayout, 0)
			desc.u32(tPictureEssenceRate, ts.Timescale)
			desc.colour(tColorPrimaries, ts.Color.Primaries)
			desc.colour(tTransferCharacteristic, ts.Color.Transfer)
			desc.colour(tCodingEquations, ts.Color.Matrix)
			desc.u8(tPictureComponentSize, 10)
			// Display rotation is a physical property of the essence and is
			// carried next to the aperture so it survives a container copy.
			desc.u16(tDisplayRotation, uint16(((ts.Rotation%360)+360)%360))
			desc.crop(ts.Crop)
		case model.KindAudio:
			desc.u32(tAudioSamplingRate, ts.Timescale)
			desc.u16(tChannelCount, 2)
			desc.u16(tAudioBitsPerSample, 24)
			desc.u8(tLocked, 1)
		}
		add(desc.build())
	}
	return body
}

func descriptorKey(k model.TrackKind) [16]byte {
	switch k {
	case model.KindAudio:
		return keyWaveAudioDesc
	case model.KindData:
		return keyDataDescriptor
	default:
		return keyCDCIDescriptor
	}
}

// localSet accumulates tagged values inside a KLV set.
type localSet struct {
	key  [16]byte
	data []byte
}

func (l *localSet) addTag(tag uint16, value []byte) {
	var hdr [4]byte
	binary.BigEndian.PutUint16(hdr[0:2], tag)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(len(value)))
	l.data = append(l.data, hdr[:]...)
	l.data = append(l.data, value...)
}

func (l *localSet) u16(tag uint16, v uint16) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	l.addTag(tag, b)
}

func (l *localSet) u8(tag uint16, v uint8) { l.addTag(tag, []byte{v}) }

func (l *localSet) u32(tag uint16, v uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	l.addTag(tag, b)
}

func (l *localSet) uid(tag uint16, v [16]byte) { l.addTag(tag, v[:]) }

func (l *localSet) text(tag uint16, s string) { l.addTag(tag, append([]byte(s), 0)) }

// colour stores a human-readable colour name so the probe can map it back to
// a canonical name; the source stays MXF.
func (l *localSet) colour(tag uint16, name string) { l.text(tag, name) }

// crop stores the aperture rectangle as four u16 values so the probe can
// reconstruct the same crop the MP4 clap box would express.
func (l *localSet) crop(c *model.Crop) {
	if c == nil {
		return
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:2], uint16(c.Left))
	binary.BigEndian.PutUint16(b[2:4], uint16(c.Top))
	binary.BigEndian.PutUint16(b[4:6], uint16(c.Right))
	binary.BigEndian.PutUint16(b[6:8], uint16(c.Bottom))
	l.addTag(tCleanAperture, b)
}

func (l *localSet) build() []byte {
	return append(klvHeader(l.key, uint64(len(l.data))), l.data...)
}

// buildIndexSegment emits an index table segment for a track. Each entry is
// 15 bytes: flags, absolute KLV offset, frame size, start tick, duration. The
// probe reads them back to build the sample table; a missing segment forces a
// scan.
func buildIndexSegment(tl *TrackLayout) []byte {
	l := &localSet{key: keyIndexSegment}
	l.u32(tTrackID, tl.Spec.TrackID)
	l.u32(tEditRate, tl.Spec.Timescale)
	l.u16(0x3F0B, uint16(tl.Spec.FrameTicks))
	var entries []byte
	for _, f := range tl.Frames {
		// 15-byte entry: flags, absolute KLV offset, frame size, start
		// tick, duration. The offset points at the KLV key, not the frame
		// header, so the demuxer can re-read and validate the wrapper.
		var e [15]byte
		if f.Sync {
			e[0] = 1
		}
		binary.BigEndian.PutUint32(e[1:5], uint32(f.Offset))
		binary.BigEndian.PutUint32(e[5:9], uint32(f.Size))
		binary.BigEndian.PutUint32(e[9:13], uint32(f.StartTick))
		binary.BigEndian.PutUint16(e[13:15], uint16(f.Duration))
		entries = append(entries, e[:]...)
	}
	l.addTag(0x3F0C, entries)
	return l.build()
}

var _ = model.ContainerMXF
