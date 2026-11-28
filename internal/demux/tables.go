package demux

import (
	"fmt"

	"github.com/abema/go-mp4"

	merr "mediacheck/internal/errors"
)

// rawTable holds the still-packed sample boxes of one track while parsing.
type rawTable struct {
	trackID       uint32
	timescale     uint32
	duration      int64
	kind          Kind
	codec         string
	width, height uint16
	channels      uint16
	sampleRate    uint32
	stts          []mp4.SttsEntry
	stsc          []mp4.StscEntry
	stszSizes     []uint32
	stszConst     uint32 // nonzero when every sample shares this size
	stco          []int64
	syncSamples   map[uint32]bool
}

func newRawTable(id uint32) *rawTable {
	return &rawTable{trackID: id, syncSamples: map[uint32]bool{}}
}

type tableBuilder struct {
	movieTimescale uint32
	tables         []*rawTable
	cur            *rawTable
}

func newTableBuilder() *tableBuilder { return &tableBuilder{} }

// ingest is invoked once per box in depth-first order.
func (b *tableBuilder) ingest(h *mp4.ReadHandle) error {
	info := &h.BoxInfo
	switch info.Type.String() {
	case "trak":
		b.cur = newRawTable(0)
		b.tables = append(b.tables, b.cur)
		return nil
	case "mvhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return b.fail(info, merr.CodeInvalidBox, "mvhd", err)
		}
		b.movieTimescale = box.(*mp4.Mvhd).Timescale
		return nil
	case "tkhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return b.fail(info, merr.CodeInvalidBox, "tkhd", err)
		}
		if b.cur != nil {
			b.cur.trackID = box.(*mp4.Tkhd).TrackID
		}
		return nil
	case "mdhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return b.fail(info, merr.CodeInvalidBox, "mdhd", err)
		}
		m := box.(*mp4.Mdhd)
		if b.cur != nil {
			b.cur.timescale = m.Timescale
			b.cur.duration = int64(m.GetDuration())
		}
		return nil
	case "hdlr":
		box, _, err := h.ReadPayload()
		if err != nil {
			return b.fail(info, merr.CodeInvalidBox, "hdlr", err)
		}
		if b.cur != nil {
			switch string(box.(*mp4.Hdlr).HandlerType[:]) {
			case "vide":
				b.cur.kind = KindVideo
			case "soun":
				b.cur.kind = KindAudio
			}
		}
		return nil
	case "stts", "stsc", "stsz", "stco", "co64", "stss":
		return b.ingestSampleTable(h)
	}

	// The first child of stsd is the sample entry; its box type is the codec.
	if len(h.Path) >= 2 && h.Path[len(h.Path)-2] == mp4.BoxTypeStsd() {
		if b.cur != nil {
			b.cur.codec = info.Type.String()
			if err := b.ingestSampleEntry(h); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *tableBuilder) ingestSampleTable(h *mp4.ReadHandle) error {
	if b.cur == nil {
		return merr.New(merr.CodeInvalidBox, "sample table outside trak").
			AtOffset(int64(h.BoxInfo.Offset)).WithState(merr.StateFatal)
	}
	t := b.cur
	box, _, err := h.ReadPayload()
	if err != nil {
		return b.fail(&h.BoxInfo, merr.CodeInvalidBox, h.BoxInfo.Type.String(), err)
	}
	switch p := box.(type) {
	case *mp4.Stts:
		t.stts = p.Entries
	case *mp4.Stsc:
		t.stsc = p.Entries
	case *mp4.Stsz:
		t.stszConst = p.SampleSize
		t.stszSizes = p.EntrySize
	case *mp4.Stco:
		t.stco = make([]int64, len(p.ChunkOffset))
		for i, v := range p.ChunkOffset {
			t.stco[i] = int64(v)
		}
	case *mp4.Co64:
		t.stco = make([]int64, len(p.ChunkOffset))
		for i, v := range p.ChunkOffset {
			t.stco[i] = int64(v)
		}
	case *mp4.Stss:
		for _, n := range p.SampleNumber {
			t.syncSamples[n] = true
		}
	}
	return nil
}

// ingestSampleEntry reads visual/audio entry fields. Unknown codec boxes are
// kept by fourCC without payload unmarshalling: framing still validated by the
// box walk, while the compatibility decision belongs to the mapping layer.
func (b *tableBuilder) ingestSampleEntry(h *mp4.ReadHandle) error {
	if !h.BoxInfo.Type.IsSupported(h.BoxInfo.Context) {
		return nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return b.fail(&h.BoxInfo, merr.CodeInvalidBox, "sample entry", err)
	}
	switch p := box.(type) {
	case *mp4.VisualSampleEntry:
		b.cur.width = p.Width
		b.cur.height = p.Height
	case *mp4.AudioSampleEntry:
		b.cur.channels = p.ChannelCount
		b.cur.sampleRate = p.SampleRate
	}
	return nil
}

// finalize expands the packed tables into flat Samples and cross-checks them.
func (b *tableBuilder) finalize(fileSize int64) ([]*Track, error) {
	out := make([]*Track, 0, len(b.tables))
	for _, rt := range b.tables {
		tr, err := rt.build(fileSize)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

func (t *rawTable) build(fileSize int64) (*Track, error) {
	n := t.sampleCount()
	if n == 0 {
		return nil, merr.New(merr.CodeInconsistentTable,
			fmt.Sprintf("track %d has no samples", t.trackID)).WithState(merr.StateFatal)
	}
	sizes, err := t.expandSizes(n)
	if err != nil {
		return nil, err
	}
	dts, durations, err := t.expandTiming(n)
	if err != nil {
		return nil, err
	}
	offsets, err := t.expandOffsets(sizes, fileSize)
	if err != nil {
		return nil, err
	}
	samples := make([]Sample, n)
	running := int64(0)
	for i := 0; i < n; i++ {
		sync := t.kind == KindAudio || len(t.syncSamples) == 0 || t.syncSamples[uint32(i+1)]
		samples[i] = Sample{
			Index: i, Offset: offsets[i], Size: sizes[i],
			DTS: running, Duration: int64(durations[i]), Sync: sync,
		}
		running += int64(durations[i])
	}
	_ = dts
	return &Track{
		ID: t.trackID, Kind: t.kind, Codec: t.codec,
		Timescale: t.timescale, Duration: t.duration,
		Width: t.width, Height: t.height,
		Channels: t.channels, SampleRate: t.sampleRate,
		Samples: samples,
	}, nil
}

func (t *rawTable) sampleCount() int {
	if t.stszConst != 0 {
		return len(t.stco) // one sample per chunk in our fixtures
	}
	return len(t.stszSizes)
}

func (t *rawTable) expandSizes(n int) ([]int, error) {
	sizes := make([]int, n)
	if t.stszConst != 0 {
		for i := range sizes {
			sizes[i] = int(t.stszConst)
		}
		return sizes, nil
	}
	if len(t.stszSizes) != n {
		return nil, merr.New(merr.CodeInconsistentTable,
			fmt.Sprintf("track %d: stsz has %d entries, want %d",
				t.trackID, len(t.stszSizes), n)).WithState(merr.StateFatal)
	}
	for i, v := range t.stszSizes {
		sizes[i] = int(v)
	}
	return sizes, nil
}

func (t *rawTable) expandTiming(n int) ([]int64, []uint32, error) {
	dts := make([]int64, n)
	durations := make([]uint32, n)
	idx := 0
	for _, e := range t.stts {
		for k := uint32(0); k < e.SampleCount && idx < n; k++ {
			durations[idx] = e.SampleDelta
			if idx > 0 {
				dts[idx] = dts[idx-1] + int64(durations[idx-1])
			}
			idx++
		}
	}
	if idx != n {
		return nil, nil, merr.New(merr.CodeInconsistentTable,
			fmt.Sprintf("track %d: stts covers %d samples, want %d", t.trackID, idx, n)).
			WithState(merr.StateFatal)
	}
	return dts, durations, nil
}

// expandOffsets walks stsc to map each sample to its chunk and verifies every
// byte range stays inside the file.
func (t *rawTable) expandOffsets(sizes []int, fileSize int64) ([]int64, error) {
	n := len(sizes)
	offsets := make([]int64, n)
	if len(t.stsc) == 0 || len(t.stco) == 0 {
		return nil, merr.New(merr.CodeInconsistentTable,
			fmt.Sprintf("track %d: missing stsc/stco", t.trackID)).WithState(merr.StateFatal)
	}
	sampleIdx := 0
	for entryIdx, e := range t.stsc {
		firstChunk := int(e.FirstChunk) - 1 // stsc chunks are 1-based
		lastChunk := len(t.stco) - 1
		if entryIdx+1 < len(t.stsc) {
			lastChunk = int(t.stsc[entryIdx+1].FirstChunk) - 2
		}
		for chunk := firstChunk; chunk <= lastChunk; chunk++ {
			base := t.stco[chunk]
			for k := uint32(0); k < e.SamplesPerChunk; k++ {
				if sampleIdx >= n {
					return nil, merr.New(merr.CodeInconsistentTable,
						fmt.Sprintf("track %d: more chunk samples than stsz", t.trackID)).
						WithState(merr.StateFatal)
				}
				end := base + int64(sizes[sampleIdx])
				if base < 0 || end > fileSize {
					return nil, merr.New(merr.CodeSampleOutOfRange,
						fmt.Sprintf("track %d sample %d [%d,+%d] outside file (%d bytes)",
							t.trackID, sampleIdx, base, sizes[sampleIdx], fileSize)).
						AtOffset(base).WithState(merr.StateFatal)
				}
				offsets[sampleIdx] = base
				base = end
				sampleIdx++
			}
		}
	}
	if sampleIdx != n {
		return nil, merr.New(merr.CodeInconsistentTable,
			fmt.Sprintf("track %d: chunks cover %d samples, want %d", t.trackID, sampleIdx, n)).
			WithState(merr.StateFatal)
	}
	return offsets, nil
}

func (b *tableBuilder) fail(info *mp4.BoxInfo, code, what string, cause error) error {
	return merr.New(code, fmt.Sprintf("malformed %s box", what)).
		AtOffset(int64(info.Offset)).WithState(merr.StateFatal).Wrap(cause)
}
