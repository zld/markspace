// Package mp4 reads track and sample timing from self-generated MP4 files.
//
// The indexer uses go-mp4 only for box parsing; all validation of sample byte
// ranges, timestamp ordering and track shape happens here. A track that fails
// validation is marked invalid with a typed error and contributes no samples:
// the kernel never invents samples for a damaged track.
package mp4

import (
	"errors"
	"io"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"media.rebuild/internal/model"
)

// Sample is one media sample in both track and nanosecond time.
type Sample struct {
	TrackID    uint32 `json:"track_id"`
	Index      int    `json:"index"`
	Offset     int64  `json:"offset"`
	Size       uint32 `json:"size"`
	DTS        int64  `json:"dts"`
	CTS        int64  `json:"cts"`
	TimeNs     int64  `json:"time_ns"`
	DurationNs int64  `json:"duration_ns"`
	Keyframe   bool   `json:"keyframe"`
}

// Track is the indexed result for one MP4 track.
type Track struct {
	TrackID   uint32         `json:"track_id"`
	Handler   string         `json:"handler"`
	Timescale uint32         `json:"timescale"`
	Duration  int64          `json:"duration"`
	Samples   []Sample       `json:"samples"`
	Valid     bool           `json:"valid"`
	Errors    []*model.Error `json:"errors"`
}

// Index is the parsed result for one MP4 file.
type Index struct {
	Source string         `json:"source"`
	Size   int64          `json:"size"`
	Tracks []Track        `json:"tracks"`
	Errors []*model.Error `json:"errors"`
}

// InvalidSamples returns the number of samples dropped because of invalid
// ranges or ordering.
func (idx *Index) InvalidSamples() int {
	n := 0
	for _, t := range idx.Tracks {
		for _, e := range t.Errors {
			if e.Code == model.CodeMP4Sample {
				n++
			}
		}
	}
	return n
}

// ValidTracks returns the number of tracks that passed validation.
func (idx *Index) ValidTracks() int {
	n := 0
	for _, t := range idx.Tracks {
		if t.Valid {
			n++
		}
	}
	return n
}

// Open indexes the MP4 at path.
func Open(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "open mp4", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "stat mp4", err)
	}
	return Read(f, path, st.Size())
}

// Read indexes MP4 data from r. rawSize is the total file size, used to bound
// sample ranges.
func Read(r io.ReadSeeker, source string, rawSize int64) (*Index, error) {
	idx := &Index{Source: source, Size: rawSize}
	if rawSize < 8 {
		idx.Errors = append(idx.Errors, model.Newf(model.CodeMP4Container, model.Source{Name: source, Offset: 0},
			"file of %d bytes cannot hold an MP4 header", rawSize))
		return idx, nil
	}
	probe, err := mp4.Probe(r)
	if err != nil {
		idx.Errors = append(idx.Errors, model.New(model.CodeMP4Container, model.Source{Name: source, Offset: 0},
			"cannot probe MP4", err))
		return idx, nil
	}
	if probe.Timescale == 0 && len(probe.Tracks) == 0 {
		idx.Errors = append(idx.Errors, model.New(model.CodeMP4Container, model.Source{Name: source, Offset: 0},
			"no movie header or tracks found", nil))
		return idx, nil
	}

	handlers, err := trackHandlers(r)
	if err != nil {
		idx.Errors = append(idx.Errors, model.New(model.CodeMP4Container, model.Source{Name: source, Offset: 0},
			"cannot read track handlers", err))
	}

	for _, pt := range probe.Tracks {
		tr := Track{
			TrackID:   pt.TrackID,
			Timescale: pt.Timescale,
			Duration:  int64(pt.Duration),
			Handler:   handlers[pt.TrackID],
			Valid:     true,
		}
		indexTrack(&tr, pt, probe.Tracks, rawSize, source)
		idx.Tracks = append(idx.Tracks, tr)
	}
	sort.Slice(idx.Tracks, func(i, j int) bool { return idx.Tracks[i].TrackID < idx.Tracks[j].TrackID })
	model.SortErrors(idx.Errors)
	return idx, nil
}

// indexTrack validates one probed track and materializes its samples.
func indexTrack(tr *Track, pt *mp4.Track, all mp4.Tracks, rawSize int64, source string) {
	if pt.Timescale == 0 {
		failTrack(tr, model.New(model.CodeMP4Track, model.Source{Name: source, Offset: -1},
			"track has zero timescale", nil))
		return
	}
	if len(pt.Samples) == 0 {
		failTrack(tr, model.New(model.CodeMP4Track, model.Source{Name: source, Offset: -1},
			"track has no samples", nil))
		return
	}
	// Expand chunk offsets and sample sizes into absolute byte ranges. A
	// sample whose range does not fit the file is dropped and reported;
	// every following sample is then suspect, so the track is marked invalid.
	offsets := make([]int64, 0, len(pt.Samples))
	cur := int64(0)
	si := 0
	for _, ch := range pt.Chunks {
		cur = int64(ch.DataOffset)
		for i := uint32(0); i < ch.SamplesPerChunk; i++ {
			if si >= len(pt.Samples) {
				break
			}
			offsets = append(offsets, cur)
			cur += int64(pt.Samples[si].Size)
			si++
		}
	}
	if len(offsets) != len(pt.Samples) {
		failTrack(tr, model.Newf(model.CodeMP4Track, model.Source{Name: source, Offset: -1},
			"chunk table describes %d samples but stts describes %d", len(offsets), len(pt.Samples)))
		return
	}

	var dts int64
	prevDTS := int64(-1)
	for i, ps := range pt.Samples {
		off := offsets[i]
		end := off + int64(ps.Size)
		if off < 0 || ps.Size == 0 || end > rawSize || end < off {
			tr.Errors = append(tr.Errors, model.Newf(model.CodeMP4Sample, model.Source{Name: source, Offset: off},
				"sample %d range [%d,%d) lies outside file of %d bytes", i, off, end, rawSize))
			tr.Valid = false
			continue
		}
		if dts <= prevDTS {
			tr.Errors = append(tr.Errors, model.Newf(model.CodeMP4Sample, model.Source{Name: source, Offset: off},
				"sample %d DTS %d does not increase past %d", i, dts, prevDTS))
			tr.Valid = false
			continue
		}
		cts := dts + ps.CompositionTimeOffset
		if cts < 0 {
			tr.Errors = append(tr.Errors, model.Newf(model.CodeMP4Sample, model.Source{Name: source, Offset: off},
				"sample %d CTS %d is negative", i, cts))
			tr.Valid = false
			continue
		}
		prevDTS = dts
		tr.Samples = append(tr.Samples, Sample{
			TrackID:    tr.TrackID,
			Index:      len(tr.Samples),
			Offset:     off,
			Size:       ps.Size,
			DTS:        dts,
			CTS:        cts,
			TimeNs:     rescale(cts, tr.Timescale),
			DurationNs: rescale(int64(ps.TimeDelta), tr.Timescale),
		})
		dts += int64(ps.TimeDelta)
	}
	if len(tr.Samples) == 0 && tr.Valid {
		failTrack(tr, model.New(model.CodeMP4Track, model.Source{Name: source, Offset: -1},
			"track produced no usable samples", nil))
		return
	}
	if !tr.Valid {
		// Keep the error trail but publish no samples from an invalid track.
		tr.Samples = nil
	}
}

func failTrack(tr *Track, err *model.Error) {
	tr.Valid = false
	tr.Errors = append(tr.Errors, err)
}

// rescale converts ticks at timescale to nanoseconds, truncating toward zero.
// Multiplication is split so a long track cannot overflow.
func rescale(ticks int64, timescale uint32) int64 {
	if timescale == 0 {
		return 0
	}
	ts := int64(timescale)
	sec := ticks / ts
	frac := ticks % ts
	const billion = int64(1_000_000_000)
	if sec > (1<<62)/billion || sec < -(1<<62)/billion {
		return 0
	}
	return sec*billion + frac*billion/ts
}

// trackHandlers extracts the hdlr handler type for each track ID.
func trackHandlers(r io.ReadSeeker) (map[uint32]string, error) {
	out := make(map[uint32]string)
	moovs, err := mp4.ExtractBox(r, nil, mp4.BoxPath{mp4.BoxTypeMoov()})
	if err != nil {
		return nil, err
	}
	if len(moovs) == 0 {
		return out, nil
	}
	traks, err := mp4.ExtractBox(r, moovs[0], mp4.BoxPath{mp4.BoxTypeTrak()})
	if err != nil {
		return nil, err
	}
	for _, trak := range traks {
		payloads, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{
			{mp4.BoxTypeTkhd()},
			{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		})
		if err != nil {
			return nil, err
		}
		var id uint32
		var handler string
		for _, p := range payloads {
			switch v := p.Payload.(type) {
			case *mp4.Tkhd:
				id = v.TrackID
			case *mp4.Hdlr:
				handler = string(v.HandlerType[:])
			}
		}
		if id != 0 {
			out[id] = handler
		}
	}
	return out, nil
}

// ErrNoTracks is returned when the file has no indexed tracks at all.
var ErrNoTracks = errors.New("mp4 has no tracks")
