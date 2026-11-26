// Package fmp4 parses fragmented MP4 (styp/moof/mdat) segments and init
// segments. It reports per-sample raw offsets, decode times and keyframe flags
// without decoding media, using go-mp4 v1.7.4.
package fmp4

import (
	"bytes"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// ErrNotFragmented marks an fMP4 file that lacks moof boxes.
var ErrNotFragmented = fmt.Errorf("not a fragmented MP4")

// Sample is one media sample with its raw byte extent in the segment file.
type Sample struct {
	TrackID           uint32
	Index             uint32 // index within its track and fragment
	Offset            int64  // raw offset of sample payload in the file
	Size              uint32
	Duration          uint32
	DecodeTime        uint64 // in the track timescale, from tfdt-origin
	CompositionOffset int32
	Keyframe          bool
}

// Track is a track declared in an init segment.
type Track struct {
	TrackID           uint32
	Timescale         uint32
	Handler           string // "vide" or "soun"
	Language          string
	Codec             string // sample entry fourcc, e.g. "mp4a", "ipcm"
	Channels          int
	SampleSize        int
	SampleRate        int
	Width             int
	Height            int
	Duration          uint64
	DefaultDuration   uint32
	DefaultSampleSize uint32
	DefaultFlags      uint32
}

// Init is the parsed initialisation segment.
type Init struct {
	Path       string
	MajorBrand string
	Tracks     []Track
}

// Fragment is one moof+mdat pair.
type Fragment struct {
	Path           string
	Sequence       uint32
	MoofOffset     int64
	MdatOffset     int64
	MdatSize       int64
	TrackID        uint32
	BaseDecodeTime uint64
	Samples        []Sample
}

// Segment is the parsed contents of one media segment file.
type Segment struct {
	Path      string
	StypBrand string
	Fragments []Fragment
	// Faults are recoverable defects; parsing stops at the first
	// non-recoverable fault.
	Faults []*fault.Error
}

// Reader opens files and exposes typed parsing. It is a struct so tests can
// substitute a read-seeking buffer.
type Reader struct {
	Path string
	f    *os.File
}

// Open opens path for parsing.
func Open(path, relPath string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, relPath, -1, "open mp4", err)
	}
	return &Reader{Path: relPath, f: f}, nil
}

// Close releases the file.
func (r *Reader) Close() error { return r.f.Close() }

// newReaderAt adapts an io.ReadSeeker for go-mp4.
func seeker(f *os.File) io.ReadSeeker { return f }

// ParseInit reads moov boxes and extracts track metadata.
func (r *Reader) ParseInit() (*Init, error) {
	init, err := parseInit(seeker(r.f), r.Path)
	if err != nil {
		return nil, err
	}
	return init, nil
}

func parseInit(rs io.ReadSeeker, path string) (*Init, error) {
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fault.Wrap(fault.KindFormat, path, -1, "seek init", err)
	}
	boxes, err := mp4.ExtractBoxesWithPayload(rs, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeIpcm()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvex(), mp4.BoxTypeTrex()},
	})
	if err != nil {
		return nil, fault.Wrap(fault.KindFormat, path, -1, "parse init boxes", err)
	}
	init := &Init{Path: path}
	type trackAcc struct {
		track mp4.Track
		tr    Track
		seen  bool
	}
	byID := map[uint32]*trackAcc{}
	order := []uint32{}
	get := func(id uint32) *trackAcc {
		if a, ok := byID[id]; ok {
			return a
		}
		a := &trackAcc{}
		byID[id] = a
		order = append(order, id)
		return a
	}
	// Reassociate boxes with their enclosing trak by walking in file order:
	// go-mp4 returns them in view order, so track-level boxes precede their
	// children. Track IDs come from tkhd.
	var cur *trackAcc
	var curID uint32
	for _, bip := range boxes {
		switch b := bip.Payload.(type) {
		case *mp4.Ftyp:
			init.MajorBrand = string(b.MajorBrand[:])
		case *mp4.Trak:
			_ = b
		case *mp4.Tkhd:
			curID = b.TrackID
			cur = get(curID)
			cur.seen = true
			cur.tr.TrackID = b.TrackID
		case *mp4.Mdhd:
			if cur == nil {
				return nil, fault.New(fault.KindFormat, path, int64(bip.Info.Offset), "mdhd outside any trak")
			}
			cur.tr.Timescale = b.Timescale
			cur.tr.Duration = b.GetDuration()
			cur.tr.Language = string(b.Language[:])
		case *mp4.Hdlr:
			if cur != nil {
				cur.tr.Handler = string(b.HandlerType[:])
			}
		case *mp4.AudioSampleEntry:
			if cur == nil {
				return nil, fault.New(fault.KindFormat, path, int64(bip.Info.Offset), "sample entry outside any trak")
			}
			cur.tr.Channels = int(b.ChannelCount)
			cur.tr.SampleSize = int(b.SampleSize)
			cur.tr.SampleRate = int(b.GetSampleRateInt())
			cur.tr.Codec = bip.Info.Type.String()
		case *mp4.VisualSampleEntry:
			if cur != nil {
				cur.tr.Width = int(b.Width)
				cur.tr.Height = int(b.Height)
				cur.tr.Codec = bip.Info.Type.String()
			}
		case *mp4.Trex:
			a := get(b.TrackID)
			a.tr.DefaultDuration = b.DefaultSampleDuration
			a.tr.DefaultSampleSize = b.DefaultSampleSize
			a.tr.DefaultFlags = b.DefaultSampleFlags
		}
	}
	for _, id := range order {
		a := byID[id]
		if !a.seen || a.tr.TrackID == 0 {
			continue
		}
		init.Tracks = append(init.Tracks, a.tr)
	}
	if len(init.Tracks) == 0 {
		return nil, fault.New(fault.KindMissing, path, size, "init segment declares no tracks").Recover(false)
	}
	return init, nil
}

// ParseSegment reads all moof/mdat pairs in a media segment. A truncated or
// corrupt fragment stops parsing at that point; complete earlier fragments are
// returned with a recoverable fault describing the exact offset.
func (r *Reader) ParseSegment() (*Segment, error) {
	return parseSegment(seeker(r.f), r.Path)
}

func parseSegment(rs io.ReadSeeker, path string) (*Segment, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fault.Wrap(fault.KindFormat, path, -1, "seek segment", err)
	}
	seg := &Segment{Path: path}
	var frag *Fragment
	sawMoof := false
	offset := int64(0)
	for {
		if _, err := rs.Seek(offset, io.SeekStart); err != nil {
			return nil, fault.Wrap(fault.KindFormat, path, offset, "seek box", err)
		}
		bi, err := mp4.ReadBoxInfo(rs)
		if err == io.EOF {
			break
		}
		if err != nil {
			fe := fault.Wrap(fault.KindTruncate, path, offset, "truncated box header", err).Recover(true)
			seg.Faults = append(seg.Faults, fe)
			break
		}
		if bi.Size < bi.HeaderSize {
			fe := fault.New(fault.KindCorrupt, path, offset, "box size smaller than header").Recover(false)
			seg.Faults = append(seg.Faults, fe)
			return seg, fe
		}
		boxEnd := int64(bi.Offset + bi.Size)
		end, seekErr := rs.Seek(0, io.SeekEnd)
		if seekErr != nil {
			return nil, fault.Wrap(fault.KindFormat, path, offset, "seek end", seekErr)
		}
		if boxEnd > end {
			fe := fault.New(fault.KindTruncate, path, offset,
				fmt.Sprintf("box %s declares %d bytes but only %d remain", bi.Type.String(), bi.Size, end-int64(bi.Offset))).
				Recover(false)
			seg.Faults = append(seg.Faults, fe)
			return seg, fe
		}
		switch bi.Type {
		case mp4.BoxTypeStyp():
			var styp mp4.Styp
			if _, err := bi.SeekToPayload(rs); err == nil {
				if _, uerr := mp4.Unmarshal(rs, bi.Size-bi.HeaderSize, &styp, bi.Context); uerr == nil {
					seg.StypBrand = string(styp.MajorBrand[:])
				}
			}
		case mp4.BoxTypeSidx():
			// sidx is not trusted for placement; fragments are located by
			// walking actual boxes. Presence is recorded through the fault
			// list only when inconsistent (handled by verify).
		case mp4.BoxTypeMoof():
			sawMoof = true
			f, ferr := parseMoof(rs, bi, path)
			if ferr != nil {
				fe, _ := fault.As(ferr)
				if fe != nil {
					seg.Faults = append(seg.Faults, fe)
				}
				return seg, ferr
			}
			seg.Fragments = append(seg.Fragments, *f)
			frag = &seg.Fragments[len(seg.Fragments)-1]
		case mp4.BoxTypeMdat():
			if frag != nil && frag.MdatOffset == 0 {
				frag.MdatOffset = int64(bi.Offset + bi.HeaderSize)
				frag.MdatSize = int64(bi.Size - bi.HeaderSize)
			}
		}
		offset = boxEnd
	}
	if !sawMoof {
		return nil, fault.New(fault.KindFormat, path, 0, "segment has no moof box").Recover(false)
	}
	// Populate per-sample raw offsets for every fragment's mdat.
	for i := range seg.Fragments {
		f := &seg.Fragments[i]
		if f.MdatOffset == 0 && f.MdatSize == 0 {
			continue
		}
		var cursor int64
		for j := range f.Samples {
			f.Samples[j].Offset = f.MdatOffset + cursor
			cursor += int64(f.Samples[j].Size)
		}
		if cursor > f.MdatSize {
			fe := fault.New(fault.KindTruncate, path, f.MdatOffset+f.MdatSize,
				fmt.Sprintf("fragment samples need %d bytes, mdat holds %d", cursor, f.MdatSize)).Recover(false)
			seg.Faults = append(seg.Faults, fe)
			return seg, fe
		}
	}
	return seg, nil
}

// parseMoof reads mfhd, traf/tfhd, tfdt and trun boxes into a Fragment.
func parseMoof(rs io.ReadSeeker, bi *mp4.BoxInfo, path string) (*Fragment, error) {
	f := &Fragment{Path: path, MoofOffset: int64(bi.Offset)}
	bips, err := mp4.ExtractBoxesWithPayload(rs, bi, []mp4.BoxPath{
		{mp4.BoxTypeMfhd()},
		{mp4.BoxTypeTraf()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	})
	if err != nil {
		return nil, fault.Wrap(fault.KindCorrupt, path, int64(bi.Offset), "parse moof", err).Recover(false)
	}
	var tfhd *mp4.Tfhd
	var tfdt *mp4.Tfdt
	var truns []*mp4.Trun
	for _, bip := range bips {
		switch b := bip.Payload.(type) {
		case *mp4.Mfhd:
			f.Sequence = b.SequenceNumber
		case *mp4.Traf:
			if f.TrackID != 0 {
				return nil, fault.New(fault.KindUnsupported, path, int64(bi.Offset),
					"multiple traf boxes in one moof are not supported").Recover(false)
			}
		case *mp4.Tfhd:
			tfhd = b
		case *mp4.Tfdt:
			tfdt = b
		case *mp4.Trun:
			truns = append(truns, b)
		}
	}
	if tfhd == nil {
		return nil, fault.New(fault.KindMissing, path, int64(bi.Offset), "moof has no tfhd").Recover(false)
	}
	f.TrackID = tfhd.TrackID
	if tfdt != nil {
		f.BaseDecodeTime = tfdt.GetBaseMediaDecodeTime()
	}
	if tfhd.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
		return nil, fault.New(fault.KindUnsupported, path, int64(bi.Offset),
			"tfhd base_data_offset is not supported; default-base-is-moof required").Recover(false)
	}
	decodeTime := f.BaseDecodeTime
	for _, trun := range truns {
		for i := uint32(0); i < trun.SampleCount; i++ {
			s := Sample{TrackID: f.TrackID, Index: i, DecodeTime: decodeTime}
			if trun.CheckFlag(0x000100) && int(i) < len(trun.Entries) {
				s.Duration = trun.Entries[i].SampleDuration
			} else {
				s.Duration = tfhd.DefaultSampleDuration
			}
			if trun.CheckFlag(0x000200) && int(i) < len(trun.Entries) {
				s.Size = trun.Entries[i].SampleSize
			} else {
				s.Size = tfhd.DefaultSampleSize
			}
			var flags uint32
			if trun.CheckFlag(0x000400) && int(i) < len(trun.Entries) {
				flags = trun.Entries[i].SampleFlags
			} else if i == 0 && trun.CheckFlag(0x000004) {
				flags = trun.FirstSampleFlags
			} else {
				flags = tfhd.DefaultSampleFlags
			}
			s.Keyframe = flags&0x00010000 == 0 // sample_is_non_sync_sample
			if trun.CheckFlag(0x000800) && int(i) < len(trun.Entries) {
				if trun.GetVersion() == 0 {
					s.CompositionOffset = int32(trun.Entries[i].SampleCompositionTimeOffsetV0)
				} else {
					s.CompositionOffset = trun.Entries[i].SampleCompositionTimeOffsetV1
				}
			}
			f.Samples = append(f.Samples, s)
			decodeTime += uint64(s.Duration)
		}
	}
	if len(f.Samples) == 0 {
		return nil, fault.New(fault.KindMissing, path, int64(bi.Offset), "moof declares no samples").Recover(false)
	}
	return f, nil
}

// ReadSample copies a sample's bytes out of the segment file. It is bounded by
// maxBytes to keep a corrupt size from allocating unbounded memory.
func (r *Reader) ReadSample(s Sample, maxBytes int64) ([]byte, error) {
	if int64(s.Size) > maxBytes {
		return nil, fault.New(fault.KindLimit, r.Path, s.Offset,
			fmt.Sprintf("sample size %d exceeds limit %d", s.Size, maxBytes)).Recover(false)
	}
	buf := make([]byte, s.Size)
	if _, err := r.f.ReadAt(buf, s.Offset); err != nil {
		return nil, fault.Wrap(fault.KindTruncate, r.Path, s.Offset, "short sample read", err).Recover(false)
	}
	return buf, nil
}

// ParseInitBytes is a helper for tests and embedded init data.
func ParseInitBytes(data []byte, path string) (*Init, error) {
	return parseInit(bytes.NewReader(data), path)
}

// ParseSegmentBytes is a helper for tests and embedded segment data.
func ParseSegmentBytes(data []byte, path string) (*Segment, error) {
	return parseSegment(bytes.NewReader(data), path)
}

// TimeOf converts a sample decode time to the canonical clock.
func (t Track) TimeOf(ticks uint64) clock.Time {
	return clock.Time{Ticks: int64(ticks), Timescale: t.Timescale}
}
