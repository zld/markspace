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
}

// ScanMP4 parses a regular or fragmented MP4 stream. Reads are bounded to the
// box walk performed by go-mp4 on the seekable blob; no whole-file buffering.
func ScanMP4(r io.ReadSeeker, sourcePath, sourceHash string) (*ScanResult, error) {
	st := &mp4State{
		res:    &ScanResult{SourcePath: sourcePath, SourceHash: sourceHash},
		tracks: map[uint32]*TrackSummary{},
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
				st.moof.trackID = p.(*mp4.Tfhd).TrackID
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
	if tr.GetFlags()&0x1 != 0 {
		fs.dataOffset = int64(tr.DataOffset)
		fs.hasDataOff = true
	}
	for _, e := range tr.Entries {
		if tr.GetFlags()&0x100 != 0 {
			fs.runDurs = append(fs.runDurs, int64(e.SampleDuration))
		}
		if tr.GetFlags()&0x200 != 0 {
			fs.runSizes = append(fs.runSizes, int64(e.SampleSize))
		}
		if tr.GetFlags()&0x800 != 0 || tr.GetFlags()&0x400 != 0 {
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
		dur := int64(0)
		if i < len(fs.runDurs) {
			dur = fs.runDurs[i]
		}
		sync := i < len(fs.runFlags) && fs.runFlags[i]&0x10000 != 0
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
