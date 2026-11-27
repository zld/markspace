package index

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"

	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/timeline"
)

// MP4TrackMeta is the keyframe-relevant metadata of one MP4 track.
type MP4TrackMeta struct {
	TrackID    uint32
	Handler    string // e.g. "vide"
	Timescale  uint32 // mdhd timescale: ticks per second
	SyncSamples []uint32 // 1-based sample numbers from stss
}

// TimeBase projects the mdhd timescale onto an integer timeline.TimeBase.
// mdhd units are 1/timescale seconds, so Num=1 Den=timescale.
func (m MP4TrackMeta) TimeBase() (timeline.TimeBase, error) {
	if m.Timescale == 0 {
		return timeline.TimeBase{}, mediaerr.New(mediaerr.StageTimebase, "",
			"track %d has zero mdhd timescale", m.TrackID)
	}
	return timeline.TimeBase{Num: 1, Den: uint64(m.Timescale)}, nil
}

// ReadMP4KeyframeMeta scans an MP4 container with go-mp4 and returns, per
// video track, its timescale and the stss sync-sample numbers that act as the
// container-side keyframe index. The elementary-stream scanners remain
// independent of this helper; it only reconciles container metadata.
func ReadMP4KeyframeMeta(r io.ReadSeeker) ([]MP4TrackMeta, error) {
	type trackState struct {
		meta    MP4TrackMeta
		inVideo bool
	}
	var (
		tracks  []*trackState
		current *trackState
	)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTrak():
			current = &trackState{meta: MP4TrackMeta{TrackID: uint32(len(tracks) + 1)}}
			tracks = append(tracks, current)
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if hdlr, ok := box.(*mp4.Hdlr); ok {
				name := string(hdlr.HandlerType[:])
				if current != nil {
					current.meta.Handler = name
					current.inVideo = name == "vide"
				}
			}
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if mdhd, ok := box.(*mp4.Mdhd); ok && current != nil {
				current.meta.Timescale = mdhd.Timescale
			}
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if stss, ok := box.(*mp4.Stss); ok && current != nil && current.inVideo {
				current.meta.SyncSamples = append(current.meta.SyncSamples,
					stss.SampleNumber...)
			}
		}
		// Recurse into every container box.
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, mediaerr.Wrap(err, mediaerr.StageIndex, "",
			"MP4 metadata scan failed")
	}
	out := make([]MP4TrackMeta, 0, len(tracks))
	for _, ts := range tracks {
		if ts.inVideo {
			out = append(out, ts.meta)
		}
	}
	return out, nil
}

// SyncSampleEntries turns stss 1-based sample numbers into index entries.
// Sample timestamps must be supplied by the caller from the stts/stsz boxes;
// this helper exists so the boundary between go-mp4 types and the kernel's
// Entry type stays explicit and testable.
func SyncSampleEntries(track string, meta MP4TrackMeta,
	sampleTS []int64) ([]Entry, error) {
	base, err := meta.TimeBase()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(meta.SyncSamples))
	for _, n := range meta.SyncSamples {
		if n == 0 || int(n) > len(sampleTS) {
			return nil, mediaerr.NewCoded(mediaerr.StageIndex, track,
				"index.stss", "sync sample %d has no timestamp", n)
		}
		entries = append(entries, Entry{
			Track:      track,
			TS:         sampleTS[n-1],
			Base:       base,
			ByteOffset: -1,
		})
	}
	return entries, nil
}

// WriteMinimalMP4 builds an in-memory MP4 containing one video track with the
// given timescale and sync-sample numbers. It is used by tests and the demo
// generator to produce a real go-mp4 container without external files.
func WriteMinimalMP4(timescale uint32, syncSamples []uint32, frameCount int) ([]byte, error) {
	buf := &bytes.Buffer{}
	ws := newWriteSeeker(buf)
	w := mp4.NewWriter(ws)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeTrak(w, timescale, syncSamples, frameCount); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTrak(w *mp4.Writer, timescale uint32, sync []uint32, frameCount int) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	// mdhd with the requested timescale and a nominal frame duration.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: timescale, DurationV0: uint32(frameCount)}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// hdlr marks this as a video track.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "vide"}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// minf/stbl/stss carrying the keyframe sample numbers.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if len(sync) > 0 {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		stss := &mp4.Stss{EntryCount: uint32(len(sync)), SampleNumber: sync}
		if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	for _, end := range []func() (*mp4.BoxInfo, error){
		w.EndBox, w.EndBox, w.EndBox, w.EndBox,
	} {
		if _, err := end(); err != nil {
			return err
		}
	}
	return nil
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker for go-mp4's Writer.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func newWriteSeeker(b *bytes.Buffer) *writeSeeker { return &writeSeeker{buf: b} }

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(w.buf.Len()) {
		need := end - int64(w.buf.Len())
		w.buf.Write(make([]byte, need))
	}
	copy(w.buf.Bytes()[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(w.buf.Len()) + offset
	}
	if w.pos < 0 {
		return 0, mediaerr.New(mediaerr.StageIndex, "", "negative seek")
	}
	return w.pos, nil
}
