package scan

import (
	"fmt"
	"io"
	"strings"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/model"
)

// ParserVersion is bumped whenever parsing semantics change. The cache/index
// layer includes it in its keys so parser upgrades force re-indexing.
const ParserVersion = "mediad-parser-1"

// mp4State accumulates boxes while ReadBoxStructure walks the tree.
type mp4State struct {
	res      *ScanResult
	tracks   map[uint32]*TrackSummary
	lastTkhd uint32
	curTrack uint32
	moof     *fragState
	fragSeq  uint32
	reader   io.ReaderAt // source for bounded NAL-header reads (sync detection)
}

type fragState struct {
	offset     int64 // absolute moof offset
	trackID    uint32
	baseTick   int64 // tfdt base media decode time
	runDurs    []int64
	runSizes   []int64
	dataOffset int64 // trun data offset, relative to moof start
	hasDataOff bool
	runFlags   []uint32
	// tfhd defaults used when trun omits per-sample fields (the common
	// ffmpeg output: durations live in tfhd default_sample_duration and the
	// keyframe flag in default_sample_flags).
	defDur      uint32
	defSize     uint32
	defFlags    uint32
	hasDefFlags bool
	durInTrun   bool // trun carries an explicit per-sample duration
	flagsInTrun bool // trun carries explicit per-sample flags
}

// ScanMP4 parses a regular or fragmented MP4 stream. Reads are bounded to the
// box walk performed by go-mp4 on the seekable blob; no whole-file buffering.
func ScanMP4(r io.ReadSeeker, sourcePath, sourceHash string) (*ScanResult, error) {
	st := &mp4State{
		res:    &ScanResult{SourcePath: sourcePath, SourceHash: sourceHash},
		tracks: map[uint32]*TrackSummary{},
	}
	// Sync detection needs random access to sample headers; every caller we
	// support (*os.File, *bytes.Reader) provides ReaderAt alongside ReadSeeker.
	if ra, ok := r.(io.ReaderAt); ok {
		st.reader = ra
	}
	_, err := mp4.ReadBoxStructure(r, st.handle)
	if err != nil {
		return nil, fmt.Errorf("scan: mp4 structure: %w", err)
	}
	for _, t := range st.tracks {
		st.res.Tracks = append(st.res.Tracks, *t)
	}
	// Stable ordering by track id keeps downstream indices deterministic.
	sortTracks(st.res.Tracks)
	return st.res, nil
}

func (st *mp4State) handle(h *mp4.ReadHandle) (interface{}, error) {
	bi := h.BoxInfo
	switch bi.Type {
	case mp4.BoxTypeTkhd():
		p, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		st.lastTkhd = p.(*mp4.Tkhd).TrackID
	case mp4.BoxTypeMdhd():
		p, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		m := p.(*mp4.Mdhd)
		if m.Timescale == 0 {
			return nil, fmt.Errorf("scan: zero timescale on track %d", st.lastTkhd)
		}
		t := st.ensureTrack(st.lastTkhd)
		t.TimeBase = model.Rational{Num: 1, Den: int64(m.Timescale)}
		t.Duration = int64(m.GetDuration())
	case mp4.BoxTypeHdlr():
		// Only the hdlr inside trak/mdia describes a track; the moov-level
		// udta/meta/hdlr ("mdir") must not overwrite the track kind.
		if !pathHasType(h.Path, mp4.BoxTypeTrak()) {
			break
		}
		p, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		hd := p.(*mp4.Hdlr)
		t := st.ensureTrack(st.lastTkhd)
		t.Handler = string(hd.HandlerType[:])
		t.Kind = hdlrKind(string(hd.HandlerType[:]))
	case mp4.BoxTypeStsd():
		// Expand once: sample-entry children also give us the codec tag.
		kids, err := h.Expand()
		if err != nil {
			return nil, err
		}
		if t := st.tracks[st.lastTkhd]; t != nil && len(kids) > 0 {
			if cb, ok := kids[0].(*mp4.BoxInfo); ok {
				t.Codec = cb.Type.String()
			}
		}
		return kids, nil
	case mp4.BoxTypeEmsg():
		st.readEmsg(h)
	case mp4.BoxTypeMoof():
		st.moof = &fragState{offset: int64(bi.Offset)}
		st.res.Fragmented = true
	case mp4.BoxTypeTfhd():
		if st.moof != nil {
			if p, _, err := h.ReadPayload(); err == nil {
				tf := p.(*mp4.Tfhd)
				st.moof.trackID = tf.TrackID
				flags := tf.GetFlags()
				if flags&mp4.TfhdDefaultSampleDurationPresent != 0 {
					st.moof.defDur = tf.DefaultSampleDuration
				}
				if flags&mp4.TfhdDefaultSampleSizePresent != 0 {
					st.moof.defSize = tf.DefaultSampleSize
				}
				if flags&mp4.TfhdDefaultSampleFlagsPresent != 0 {
					st.moof.defFlags = tf.DefaultSampleFlags
					st.moof.hasDefFlags = true
				}
			}
		}
	case mp4.BoxTypeTfdt():
		if st.moof != nil {
			if p, _, err := h.ReadPayload(); err == nil {
				st.moof.baseTick = int64(p.(*mp4.Tfdt).GetBaseMediaDecodeTime())
			}
		}
	case mp4.BoxTypeTrun():
		st.collectTrun(h)
	case mp4.BoxTypeMdat():
		st.materializeFragment(bi)
	}
	// Recurse into every container; leaves return an empty expansion.
	return h.Expand()
}

func (st *mp4State) ensureTrack(id uint32) *TrackSummary {
	if t, ok := st.tracks[id]; ok {
		return t
	}
	t := &TrackSummary{ID: id}
	st.tracks[id] = t
	return t
}

// pathHasType reports whether a box path contains a given ancestor type.
func pathHasType(path mp4.BoxPath, want mp4.BoxType) bool {
	for _, b := range path {
		if b == want {
			return true
		}
	}
	return false
}

// collectTrun captures per-sample durations, sizes and flags from a fragment.
func (st *mp4State) collectTrun(h *mp4.ReadHandle) {
	if st.moof == nil {
		return
	}
	p, _, err := h.ReadPayload()
	if err != nil {
		return
	}
	tr := p.(*mp4.Trun)
	fs := st.moof
	trFlags := tr.GetFlags()
	if trFlags&0x1 != 0 {
		fs.dataOffset = int64(tr.DataOffset)
		fs.hasDataOff = true
	}
	fs.durInTrun = trFlags&0x100 != 0
	fs.flagsInTrun = trFlags&0x400 != 0
	sizeInTrun := trFlags&0x200 != 0
	for _, e := range tr.Entries {
		if fs.durInTrun {
			fs.runDurs = append(fs.runDurs, int64(e.SampleDuration))
		}
		// Size: prefer the trun entry; fall back to the tfhd default size.
		if sizeInTrun {
			fs.runSizes = append(fs.runSizes, int64(e.SampleSize))
		} else {
			fs.runSizes = append(fs.runSizes, int64(fs.defSize))
		}
		if fs.flagsInTrun {
			fs.runFlags = append(fs.runFlags, e.SampleFlags)
		}
	}
}

// materializeFragment turns a buffered trun into concrete samples once the
// following mdat reveals its byte offset.
func (st *mp4State) materializeFragment(bi mp4.BoxInfo) {
	fs := st.moof
	if fs == nil || len(fs.runSizes) == 0 {
		st.moof = nil
		return
	}
	t := st.ensureTrack(fs.trackID)
	t.Fragmented = true
	var off int64
	if fs.hasDataOff {
		off = fs.offset + fs.dataOffset
	} else {
		off = int64(bi.Offset + bi.HeaderSize)
	}
	tick := fs.baseTick
	for i, size := range fs.runSizes {
		// Duration: explicit trun value when present, else the tfhd default.
		var dur int64
		if fs.durInTrun && i < len(fs.runDurs) {
			dur = fs.runDurs[i]
		} else {
			dur = int64(fs.defDur)
		}
		// Sync detection: prefer an explicit trun/tfhd sample flag, then fall
		// back to inspecting the sample's leading NAL units for an IDR slice.
		var flags uint32
		if fs.flagsInTrun && i < len(fs.runFlags) {
			flags = fs.runFlags[i]
		} else if fs.hasDefFlags {
			flags = fs.defFlags
		}
		sync := isSyncSample(flags)
		if !fs.flagsInTrun && st.reader != nil {
			if nal, ok := st.sampleHasIDR(off, size); ok {
				sync = nal
			}
		}
		st.res.Samples = append(st.res.Samples, Sample{
			TrackID: fs.trackID, Tick: tick, DurTick: dur,
			Offset: off, Length: size, IsSync: sync,
		})
		t.SampleCount++
		tick += dur
		off += size
	}
	st.fragSeq++
	st.moof = nil
}

// maxNALHeaders bounds the number of NAL headers inspected in one sample. The
// IDR/parameter-set NALs always precede slice data, so only the leading headers
// matter; this caps reads regardless of individual NAL size.
const maxNALHeaders = 16

// sampleHasIDR reports whether an avcC length-prefixed sample contains an
// H.264 IDR slice NAL (type 5). It reads only each NAL's 4-byte length prefix
// and one type byte and then seeks past the payload, so a large leading SEI
// NAL cannot hide the IDR that follows it and total reads stay bounded.
func (st *mp4State) sampleHasIDR(offset, length int64) (bool, bool) {
	pos := offset
	end := offset + length
	hdr := make([]byte, 5)
	for n := 0; n < maxNALHeaders && pos+5 <= end; n++ {
		if m, err := st.reader.ReadAt(hdr, pos); err != nil || m != 5 {
			return false, false
		}
		nalLen := int64(hdr[0])<<24 | int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		if nalLen <= 0 || pos+4+nalLen > end {
			break
		}
		if hdr[4]&0x1F == 5 { // IDR slice
			return true, true
		}
		pos += 4 + nalLen
	}
	return false, true
}

// isSyncSample interprets ISO 14496 sample flags. The sample_depends_on field
// occupies bits 24-25 of the full 32-bit flags; value 1 means the sample does
// depend on others (not sync), value 2 means it does not (a sync/key sample),
// 0 is unknown. ffmpeg's default_sample_flags of 0x1010000 marks non-key
// samples, while a key frame's flags are 0x02000000 (depends_on=2) or zero.
func isSyncSample(flags uint32) bool {
	dependsOn := (flags >> 24) & 0x3
	switch dependsOn {
	case 1:
		return false // depends on others
	case 2:
		return true // independent / sync
	default:
		// Unknown: treat the common ffmpeg non-key marker (bit 24 set in the
		// low region) as non-sync, otherwise assume sync.
		return flags&0x01000000 == 0
	}
}

// hdlrKind maps an ISOBMFF handler type to our stable track kind.
func hdlrKind(handler string) string {
	switch strings.TrimRight(handler, "\x00") {
	case "vide":
		return KindVideo
	case "soun":
		return KindAudio
	case "text", "subt":
		return KindSubtitle
	case "meta":
		return KindMetadata
	default:
		return KindMetadata
	}
}

func sortTracks(ts []TrackSummary) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j-1].ID > ts[j].ID; j-- {
			ts[j-1], ts[j] = ts[j], ts[j-1]
		}
	}
}
