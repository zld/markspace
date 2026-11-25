package mp4

import (
	"encoding/binary"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/timeline"
)

// trun/tfhd flag bits from ISO/IEC 14496-12.
const (
	// FlagTrunDataOffset marks the trun data-offset field.
	FlagTrunDataOffset uint32 = 0x000001
	flagTrunFirstFlags uint32 = 0x000004
	flagTrunDuration   uint32 = 0x000100
	flagTrunSize       uint32 = 0x000200
	flagTrunFlags      uint32 = 0x000400
	flagTrunCompOffset uint32 = 0x000800
	flagTfhdDuration   uint32 = 0x000008
	flagTfhdSize       uint32 = 0x000010
	flagTfhdDefaultIsMoof uint32 = 0x020000
)

// Sample is one media sample located inside an mdat box.
type Sample struct {
	Index       int   // 0-based within its track fragment
	DataOffset  int64 // absolute byte offset of sample data in the file
	Size        int64 // sample data length in bytes
	Duration    int64 // duration in track timescale ticks (0 if unknown)
	CompOffset  int64 // composition-time offset in ticks (0 if absent)
	IsSync      bool  // key sample when sample flags mark one
}

// TrackFragment is the decoded content of one traf box.
type TrackFragment struct {
	TrackID      uint32
	BaseMediaT   uint64 // tfdt base media decode time, track timescale ticks
	Timescale    uint32 // track timescale, 0 when not declared in this file
	Handler      string // "vide"/"snd"/"" from moov when available
	Samples      []Sample
	TrunOffset   int64 // trun-declared data offset (relative to moof), 0 if absent
	TrunBox      *Box  // trun box location, nil when absent
	TrunFlags    uint32
}

// Fragment is one moof/mdat pair (a CMAF chunk), fully located.
type Fragment struct {
	File        string
	SeqNum      uint32 // mfhd sequence number
	Moof        *Box
	Mdat        *Box
	Tracks      []*TrackFragment
	// ContentRange spans every byte covered by moof+mdat.
	ContentRange [2]int64
}

func fullBoxFlags(b [3]byte) uint32 {
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

func unmarshalBox(r io.ReadSeeker, b *Box, dst gomp4.IBox) error {
	if _, err := r.Seek(b.PayloadOff, io.SeekStart); err != nil {
		return err
	}
	if _, err := gomp4.Unmarshal(r, uint64(b.PayloadLen), dst, gomp4.Context{}); err != nil {
		return timeline.New(timeline.ErrInvalidInput, "", b.Offset,
			"cannot decode %s box: %v", b.Type, err)
	}
	return nil
}

func mustChild(parent *Box, typ, file string) (*Box, error) {
	c := parent.FindChild(typ)
	if c == nil {
		return nil, timeline.New(timeline.ErrMissingField, file, parent.Offset,
			"required %q box missing inside %q", typ, parent.Type)
	}
	return c, nil
}

// DecodeFragment decodes one moof box and binds it to its mdat payload,
// validating every declared sample range. r reads the same file the boxes
// were scanned from.
func DecodeFragment(r io.ReadSeeker, file string, moof, mdat *Box, lim Limits) (*Fragment, error) {
	lim = lim.orDefaults()
	mfhdBox, err := mustChild(moof, "mfhd", file)
	if err != nil {
		return nil, err
	}
	var mfhd gomp4.Mfhd
	if err := unmarshalBox(r, mfhdBox, &mfhd); err != nil {
		return nil, err
	}
	frag := &Fragment{File: file, SeqNum: mfhd.SequenceNumber, Moof: moof, Mdat: mdat}

	trafs := childrenOf(moof, "traf")
	if len(trafs) == 0 {
		return nil, timeline.New(timeline.ErrMissingField, file, moof.Offset, "moof contains no traf")
	}
	for _, traf := range trafs {
		tf, err := decodeTraf(r, file, moof, mdat, traf, lim)
		if err != nil {
			return nil, err
		}
		frag.Tracks = append(frag.Tracks, tf)
	}
	if mdat != nil {
		frag.ContentRange = [2]int64{moof.Offset, mdat.End()}
	} else {
		frag.ContentRange = [2]int64{moof.Offset, moof.End()}
	}
	return frag, nil
}

func childrenOf(b *Box, typ string) []*Box {
	var out []*Box
	for _, c := range b.Children {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

func decodeTraf(r io.ReadSeeker, file string, moof, mdat, traf *Box, lim Limits) (*TrackFragment, error) {
	tfhdBox, err := mustChild(traf, "tfhd", file)
	if err != nil {
		return nil, err
	}
	var tfhd gomp4.Tfhd
	if err := unmarshalBox(r, tfhdBox, &tfhd); err != nil {
		return nil, err
	}
	tf := &TrackFragment{TrackID: tfhd.TrackID}
	tfhdFlags := fullBoxFlags(tfhd.Flags)

	if tfdtBox := traf.FindChild("tfdt"); tfdtBox != nil {
		var tfdt gomp4.Tfdt
		if err := unmarshalBox(r, tfdtBox, &tfdt); err != nil {
			return nil, err
		}
		if tfdt.Version == 1 {
			tf.BaseMediaT = tfdt.BaseMediaDecodeTimeV1
		} else {
			tf.BaseMediaT = uint64(tfdt.BaseMediaDecodeTimeV0)
		}
	}

	trunBox := traf.FindChild("trun")
	if trunBox == nil {
		// A traf without trun carries no samples; still valid metadata.
		return tf, nil
	}
	var trun gomp4.Trun
	if err := unmarshalBox(r, trunBox, &trun); err != nil {
		return nil, err
	}
	trunFlags := fullBoxFlags(trun.Flags)
	tf.TrunBox = trunBox
	tf.TrunFlags = trunFlags
	if int(trun.SampleCount) > lim.MaxSamples {
		return nil, timeline.New(timeline.ErrLimitExceeded, file, trunBox.Offset,
			"trun declares %d samples, limit %d", trun.SampleCount, lim.MaxSamples)
	}
	if trunFlags&flagTrunSize == 0 && tfhdFlags&flagTfhdSize == 0 {
		return nil, timeline.New(timeline.ErrMissingField, file, trunBox.Offset,
			"sample sizes unavailable: trun lacks sample-size flag and tfhd has no default size")
	}
	if mdat == nil {
		return nil, timeline.New(timeline.ErrMissingField, file, moof.Offset,
			"moof/traf declares samples but no mdat follows")
	}

	// Data offset is relative to the start of the enclosing moof.
	var dataOff int64
	switch {
	case trunFlags&FlagTrunDataOffset != 0:
		dataOff = moof.Offset + int64(trun.DataOffset)
		tf.TrunOffset = int64(trun.DataOffset)
	case tfhdFlags&flagTfhdDefaultIsMoof != 0:
		dataOff = mdat.PayloadOff
	default:
		return nil, timeline.New(timeline.ErrMissingField, file, trunBox.Offset,
			"trun has no data-offset and tfhd default-base-is-moof is not set")
	}

	cursor := dataOff
	for i := 0; i < int(trun.SampleCount); i++ {
		entry := trun.Entries[i]
		size := int64(entry.SampleSize)
		if trunFlags&flagTrunSize == 0 {
			size = int64(tfhd.DefaultSampleSize)
		}
		if size <= 0 {
			return nil, timeline.New(timeline.ErrInvalidInput, file, trunBox.Offset,
				"sample %d has non-positive size %d", i, size)
		}
		if cursor < mdat.PayloadOff || cursor+size > mdat.End() {
			return nil, timeline.New(timeline.ErrBoxTruncated, file, cursor,
				"sample %d range [%d,%d) escapes mdat [%d,%d)",
				i, cursor, cursor+size, mdat.PayloadOff, mdat.End())
		}
		dur := int64(0)
		if trunFlags&flagTrunDuration != 0 {
			dur = int64(entry.SampleDuration)
		} else if tfhdFlags&flagTfhdDuration != 0 {
			dur = int64(tfhd.DefaultSampleDuration)
		}
		comp := int64(0)
		if trunFlags&flagTrunCompOffset != 0 {
			if trun.Version == 0 {
				comp = int64(entry.SampleCompositionTimeOffsetV0)
			} else {
				comp = int64(entry.SampleCompositionTimeOffsetV1)
			}
		}
		sync := true
		if trunFlags&flagTrunFlags != 0 {
			sync = (entry.SampleFlags>>16)&0x1 == 0 // sample_depends_on / non-sync bit
		}
		tf.Samples = append(tf.Samples, Sample{
			Index: i, DataOffset: cursor, Size: size,
			Duration: dur, CompOffset: comp, IsSync: sync,
		})
		cursor += size
	}
	return tf, nil
}

// TrackMeta carries moov-level track metadata needed for time conversion.
type TrackMeta struct {
	TrackID   uint32
	Timescale uint32
	Handler   string
}

// ParseMoov extracts timescale/handler per track when a moov box is present.
func ParseMoov(r io.ReadSeeker, moov *Box) ([]TrackMeta, error) {
	var metas []TrackMeta
	for _, trak := range childrenOf(moov, "trak") {
		mdhdBox := trak.FindPath("mdia", "mdhd")
		hdlrBox := trak.FindPath("mdia", "hdlr")
		if mdhdBox == nil || hdlrBox == nil {
			continue
		}
		var mdhd gomp4.Mdhd
		if err := unmarshalBox(r, mdhdBox, &mdhd); err != nil {
			return nil, err
		}
		var hdlr gomp4.Hdlr
		if err := unmarshalBox(r, hdlrBox, &hdlr); err != nil {
			return nil, err
		}
		// tkhd track_id; fall back to declaration order when absent.
		id := uint32(len(metas) + 1)
		if tkhd := trak.FindPath("tkhd"); tkhd != nil {
			if v, ok := readTrackID(r, tkhd); ok {
				id = v
			}
		}
		metas = append(metas, TrackMeta{
			TrackID: id, Timescale: mdhd.Timescale, Handler: string(hdlr.HandlerType[:]),
		})
	}
	return metas, nil
}

func readTrackID(r io.ReadSeeker, tkhd *Box) (uint32, bool) {
	buf, err := ReadPayload(r, tkhd)
	if err != nil || len(buf) < 24 {
		return 0, false
	}
	// FullBox(4) + creation(4/8) + modification(4/8) + track_ID(4)
	off := 4
	if buf[0] == 1 {
		off += 16
	} else {
		off += 8
	}
	if off+4 > len(buf) {
		return 0, false
	}
	return binary.BigEndian.Uint32(buf[off : off+4]), true
}
