package archive

import (
	"bytes"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediaarchive/internal/ebml"
	"mediaarchive/internal/pipeline"
	"mediaarchive/internal/timeline"
)

// WriteRecoveredMatroska serializes merged events back into a minimal
// Matroska container: an EBML header, one unknown-length Segment and one
// Cluster per distinct event time. It reuses the ebml encoder so output is
// produced only by audited, shared code.
func WriteRecoveredMatroska(events []pipeline.Event) []byte {
	// Group events by their timestamp to form clusters.
	var clusterTimes []int64
	byTime := map[int64][]pipeline.Event{}
	for _, ev := range events {
		if _, ok := byTime[ev.TimeNanos]; !ok {
			clusterTimes = append(clusterTimes, ev.TimeNanos)
		}
		byTime[ev.TimeNanos] = append(byTime[ev.TimeNanos], ev)
	}
	// events arrive already merged and sorted, so clusterTimes is ordered.
	var clusters [][]byte
	for _, tc := range clusterTimes {
		var blocks [][]byte
		for _, ev := range byTime[tc] {
			payload := ev.Payload
			if len(payload) == 0 {
				// Placeholder for recovered/gap slots: keep a marker byte so
				// the block is structurally valid.
				payload = []byte{0}
			}
			blocks = append(blocks, ebml.SimpleBlock(ev.Track, 0, true, payload))
		}
		// Cluster timecode is in milliseconds.
		clusters = append(clusters, ebml.Cluster(uint64(tc/1_000_000), blocks...))
	}
	return ebml.BuildMatroska(clusters, true) // unknown-length segment
}

// writeSeeker is an in-memory io.WriteSeeker backed by a growable byte slice.
type writeSeeker struct {
	buf []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(cap(w.buf)) {
		grown := make([]byte, end, end*2)
		copy(grown, w.buf[:w.pos])
		w.buf = grown
	} else if end > int64(len(w.buf)) {
		w.buf = w.buf[:end]
	}
	copy(w.buf[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = w.pos + offset
	case io.SeekEnd:
		target = int64(len(w.buf)) + offset
	}
	if target < 0 || target > int64(cap(w.buf)) {
		return 0, fmt.Errorf("seek out of range: %d", target)
	}
	w.pos = target
	if target > int64(len(w.buf)) {
		w.buf = w.buf[:target]
	}
	return target, nil
}

func (w *writeSeeker) bytes() []byte { return w.buf[:w.pos] }

// BuildArchiveIndex writes a minimal, structurally valid MP4 index (ftyp +
// moov/mvhd skeleton) that the archive system uses to cross-check the
// manifest summary. go-mp4 performs the actual box encoding.
func BuildArchiveIndex() ([]byte, error) {
	mem := &writeSeeker{}
	w := mp4.NewWriter(mem)

	// ftyp box: a valid file-type box the verifier can parse.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'a', 't', 'r'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// moov > mvhd skeleton: proves nested-box round-tripping works.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return nil, err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  0,
		Rate:        0x00010000, // 1.0 fixed point
		Volume:      0x0100,     // 1.0 fixed point
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: 1,
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // mvhd
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return mem.bytes(), nil
}

// IndexReport is the verifier's structural summary.
type IndexReport struct {
	BoxCount int
	HasFtyp  bool
	HasMoov  bool
	HasMvhd  bool
}

// VerifyArchiveIndex parses the MP4 index with go-mp4 and confirms the
// expected box hierarchy is present and well-formed.
func VerifyArchiveIndex(index []byte) (*IndexReport, error) {
	r := bytes.NewReader(index)
	rep := &IndexReport{}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		rep.BoxCount++
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			rep.HasFtyp = true
		case mp4.BoxTypeMoov():
			rep.HasMoov = true
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case mp4.BoxTypeMvhd():
			rep.HasMvhd = true
		}
		return nil, nil
	})
	if err != nil {
		return nil, timeline.NewError("archive", "verify_index", -1,
			timeline.RecoverNone, err.Error(), timeline.ErrOutOfBounds)
	}
	if !rep.HasFtyp || !rep.HasMoov || !rep.HasMvhd {
		return rep, timeline.NewError("archive", "verify_index", -1,
			timeline.RecoverNone,
			fmt.Sprintf("incomplete index: ftyp=%v moov=%v mvhd=%v",
				rep.HasFtyp, rep.HasMoov, rep.HasMvhd),
			timeline.ErrOutOfBounds)
	}
	return rep, nil
}
