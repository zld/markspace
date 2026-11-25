package media

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/internal/mp4ext"
)

// This file turns stsd sample entries into raw Description values. Every entry
// becomes one Description with its stsd index as priority; conflicts between
// entries are counted by Track.Normalize, never resolved here.

// parseMP4Descriptions expands stsd entries into Description values.
func parseMP4Descriptions(data []byte, track *Track, tkhd *mp4.Tkhd, stbl *boxNode) error {
	stsdNode := stbl.find(mp4.BoxTypeStsd())
	if stsdNode == nil {
		return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stbl.offset, "mp4: missing stsd")
	}
	entries, err := expandStsd(data, stsdNode, track.ID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stsdNode.offset, "mp4: empty stsd")
	}
	var rotation int
	if track.Kind == KindVideo {
		rotation, err = rotationFromMatrix(tkhd.Matrix)
		if err != nil {
			return merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, track.InputOffset,
				"mp4: %v", err)
		}
	}
	for i, entry := range entries {
		desc := Description{
			Priority: i,
			Origin:   fmt.Sprintf("stsd[%d]", i),
		}
		switch track.Kind {
		case KindVideo:
			if entry.video == nil {
				return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stsdNode.offset,
					"mp4: stsd entry %d is not a visual sample entry", i)
			}
			desc.Width = uint32(entry.video.Width)
			desc.Height = uint32(entry.video.Height)
			r := rotation
			desc.Rotation = &r
			crop, err := mp4Crop(entry.clap, track.ID, stsdNode.offset)
			if err != nil {
				return err
			}
			desc.Crop = crop
			if len(entry.pasp) > 0 {
				desc.Aspect = &Aspect{HSpacing: entry.pasp[0].HSpacing, VSpacing: entry.pasp[0].VSpacing}
			}
			colour, err := mp4Colour(entry.colr, track.ID, stsdNode.offset)
			if err != nil {
				return err
			}
			desc.Colour = colour
		case KindAudio:
			if entry.audio == nil {
				return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stsdNode.offset,
					"mp4: stsd entry %d is not an audio sample entry", i)
			}
			desc.Rate = entry.audio.SampleRate >> 16
			desc.Channels = entry.audio.ChannelCount
			desc.Layout = layoutForChannels(entry.audio.ChannelCount)
		}
		track.Descriptions = append(track.Descriptions, desc)
	}
	if track.Kind == KindVideo && len(entries) > 1 {
		// Multiple active entries are legal in the profile; each contributes a
		// priority. The normalizer counts every disagreement.
	}
	return nil
}

// expandStsd decodes each sample entry plus its child boxes.
func expandStsd(data []byte, stsdNode *boxNode, trackID uint32) ([]mp4Entry, error) {
	ps := stsdNode.payloadStart()
	if stsdNode.payloadSize() < 8 {
		return nil, merr.New(merr.StageProbe, merr.KindTruncated, trackID, ps, "mp4: stsd payload too short")
	}
	count := int(binary.BigEndian.Uint32(data[ps+4:]))
	end := ps + stsdNode.payloadSize()
	pos := ps + 8
	var out []mp4Entry
	for i := 0; i < count; i++ {
		if pos+8 > end {
			return nil, merr.New(merr.StageProbe, merr.KindTruncated, trackID, pos,
				"mp4: stsd declares %d entries but entry %d is missing", count, i)
		}
		size := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := mp4.BoxType{data[pos+4], data[pos+5], data[pos+6], data[pos+7]}
		if size < 8 || pos+size > end {
			return nil, merr.New(merr.StageProbe, merr.KindCorrupt, trackID, pos,
				"mp4: stsd entry %d (%s) has invalid size %d", i, typ.String(), size)
		}
		entry, err := decodeSampleEntry(data, pos, size, typ, trackID)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
		pos += size
	}
	return out, nil
}

func decodeSampleEntry(data []byte, pos, size int64, typ mp4.BoxType, trackID uint32) (mp4Entry, error) {
	payload := data[pos+8 : pos+size]
	entry := mp4Entry{}
	box, err := typ.New(mp4.Context{})
	if err != nil {
		return entry, merr.Wrap(merr.StageProbe, merr.KindParse, trackID, pos, err,
			"mp4: sample entry %s", typ.String())
	}
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), box, mp4.Context{}); err != nil {
		return entry, merr.Wrap(merr.StageProbe, merr.KindParse, trackID, pos+8, err,
			"mp4: sample entry %s payload", typ.String())
	}
	var childStart int64
	switch b := box.(type) {
	case *mp4.VisualSampleEntry:
		entry.video = b
		childStart = 8 + 78 // SampleEntry + visual fields
	case *mp4.AudioSampleEntry:
		entry.audio = b
		childStart = 8 + 20 // SampleEntry + audio fields (v0)
	default:
		// Unknown entry types still yield a usable description with no extras.
		return entry, nil
	}
	if childStart >= size {
		return entry, nil
	}
	kids, err := walkBoxes(data[pos+childStart:pos+size], pos+childStart, 0)
	if err != nil {
		// Unknown trailing bytes in a sample entry are tolerated only when they
		// do not look like boxes at all; otherwise the truncation matters.
		return entry, nil
	}
	for _, k := range kids {
		switch k.typ {
		case mp4.BoxTypeColr():
			c, err := decodeNode(data, k, mp4.Context{})
			if err != nil {
				return entry, err
			}
			entry.colr = append(entry.colr, c.(*mp4.Colr))
		case mp4ext.BoxTypeClap():
			c, err := decodeNode(data, k, mp4.Context{})
			if err != nil {
				return entry, err
			}
			entry.clap = append(entry.clap, c.(*mp4ext.Clap))
		case mp4.BoxTypePasp():
			c, err := decodeNode(data, k, mp4.Context{})
			if err != nil {
				return entry, err
			}
			entry.pasp = append(entry.pasp, c.(*mp4.PixelAspectRatioBox))
		}
	}
	return entry, nil
}

func mp4Crop(claps []*mp4ext.Clap, trackID uint32, offset int64) (*Crop, error) {
	if len(claps) == 0 {
		return nil, nil
	}
	if len(claps) > 1 {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, trackID, offset,
			"mp4: sample entry carries %d clap boxes", len(claps))
	}
	c := claps[0]
	w, err := c.Width()
	if err != nil {
		return nil, merr.Wrap(merr.StageMetadata, merr.KindInconsistent, trackID, offset, err, "mp4: clap")
	}
	h, err := c.Height()
	if err != nil {
		return nil, merr.Wrap(merr.StageMetadata, merr.KindInconsistent, trackID, offset, err, "mp4: clap")
	}
	hx, err := c.HorizOffset()
	if err != nil {
		return nil, merr.Wrap(merr.StageMetadata, merr.KindInconsistent, trackID, offset, err, "mp4: clap")
	}
	vy, err := c.VertOffset()
	if err != nil {
		return nil, merr.Wrap(merr.StageMetadata, merr.KindInconsistent, trackID, offset, err, "mp4: clap")
	}
	return &Crop{Width: w, Height: h, HorizOffset: hx, VertOffset: vy}, nil
}

func mp4Colour(colrs []*mp4.Colr, trackID uint32, offset int64) (*Colour, error) {
	if len(colrs) == 0 {
		return nil, nil
	}
	if len(colrs) > 1 {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, trackID, offset,
			"mp4: sample entry carries %d colr boxes", len(colrs))
	}
	c := colrs[0]
	if c.ColourType != [4]byte{'n', 'c', 'l', 'x'} {
		// ICC/prof descriptions are preserved as zero attributes rather than
		// misread as nclx primaries.
		return &Colour{}, nil
	}
	if c.ColourPrimaries == 2 || c.TransferCharacteristics == 2 || c.MatrixCoefficients == 2 {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, trackID, offset,
			"mp4: colr uses unspecified (2) colour code")
	}
	return &Colour{
		Primaries: c.ColourPrimaries,
		Transfer:  c.TransferCharacteristics,
		Matrix:    c.MatrixCoefficients,
		FullRange: c.FullRangeFlag,
	}, nil
}

// mp4Entry is one decoded stsd sample entry plus its child description boxes.
type mp4Entry struct {
	video *mp4.VisualSampleEntry
	audio *mp4.AudioSampleEntry
	colr  []*mp4.Colr
	clap  []*mp4ext.Clap
	pasp  []*mp4.PixelAspectRatioBox
}

// parseMP4VideoTiming validates the video timing table against the mdhd
// duration. Video essence is not part of the published audio timeline, so only
// structural consistency is enforced here.
func parseMP4VideoTiming(data []byte, track *Track, stbl *boxNode) error {
	sttsNode := stbl.find(mp4.BoxTypeStts())
	if sttsNode == nil {
		return merr.New(merr.StageMetadata, merr.KindParse, track.ID, stbl.offset, "mp4: video stbl without stts")
	}
	if err := checkSTTSPayloadSize(data, sttsNode, track.ID); err != nil {
		return err
	}
	box, err := decodeNode(data, sttsNode, mp4.Context{})
	if err != nil {
		return err
	}
	stts := box.(*mp4.Stts)
	var samples int64
	for _, e := range stts.Entries {
		samples += int64(e.SampleCount)
	}
	return checkSTTS(stts, track, samples, sttsNode.offset)
}
