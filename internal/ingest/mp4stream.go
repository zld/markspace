package ingest

import (
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"

	"avcheck/internal/media"
)

// MP4Builder streams samples into one MP4 video track. Samples are appended
// to a temp payload file, so memory stays constant regardless of track
// length; the sample table is finalized by Close.
type MP4Builder struct {
	path    string
	tmpPath string
	data    *os.File
	sizes   []uint32
	keys    []uint32 // 1-based sample numbers
	durs    []uint32
	lastDTS int64
	count   int
	started bool
	failed  error
}

// NewMP4Builder creates a builder writing to path.
func NewMP4Builder(path string) (*MP4Builder, error) {
	tmp := path + ".mdat.tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	return &MP4Builder{path: path, tmpPath: tmp, data: f}, nil
}

// Add appends one frame. Ticks must strictly increase; the duration of each
// sample is the delta to the next, and the final sample reuses the last
// delta, so every sample carries a positive duration.
func (b *MP4Builder) Add(f *media.Frame) error {
	if b.failed != nil {
		return b.failed
	}
	if !b.started {
		b.started = true
	} else if f.Ticks <= b.lastDTS {
		return media.NewError(media.KindRange, f.StreamID, f.Seq, -1,
			"timestamp regression: ticks %d after %d", f.Ticks, b.lastDTS)
	}
	if len(f.Payload) > int(^uint32(0)) {
		return media.NewError(media.KindOverflow, f.StreamID, f.Seq, -1, "sample size overflow")
	}
	if b.count > 0 {
		d := f.Ticks - b.lastDTS
		if d > int64(^uint32(0)) {
			return media.NewError(media.KindOverflow, f.StreamID, f.Seq, -1, "delta %d overflows uint32", d)
		}
		b.durs[b.count-1] = uint32(d)
	}
	if _, err := b.data.Write(f.Payload); err != nil {
		b.failed = err
		return err
	}
	b.sizes = append(b.sizes, uint32(len(f.Payload)))
	if f.Key {
		b.keys = append(b.keys, uint32(b.count+1))
	}
	b.durs = append(b.durs, 1)
	b.lastDTS = f.Ticks
	b.count++
	return nil
}

// Count returns the number of samples added so far.
func (b *MP4Builder) Count() int { return b.count }

// Close finalizes the sample table and writes the MP4. It is idempotent on
// the temp file.
func (b *MP4Builder) Close(timescale uint32) error {
	defer func() {
		b.data.Close()
		os.Remove(b.tmpPath)
	}()
	if b.failed != nil {
		return b.failed
	}
	if b.count == 0 || timescale == 0 {
		return media.NewError(media.KindRange, 0, 0, -1, "empty sample set or zero timescale")
	}
	// The last sample has no following timestamp: reuse the previous delta so
	// every sample carries a positive duration (a single sample uses 1).
	if b.count > 1 {
		b.durs[b.count-1] = b.durs[b.count-2]
	}
	if _, err := b.data.Seek(0, 0); err != nil {
		return err
	}
	return b.writeHeader(timescale)
}

// writeHeader composes ftyp, moov (with the complete sample table) and mdat
// header, then splices in the temp payload file.
func (b *MP4Builder) writeHeader(timescale uint32) (err error) {
	var stts []mp4.SttsEntry
	for _, d := range b.durs {
		if n := len(stts); n > 0 && stts[n-1].SampleDelta == d {
			stts[n-1].SampleCount++
		} else {
			stts = append(stts, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	var total uint64
	for _, s := range b.sizes {
		total += uint64(s)
	}
	if total > uint64(^uint32(0))-8 {
		return media.NewError(media.KindOverflow, 0, 0, -1, "mdat size %d overflow", total)
	}
	var duration int64
	for _, d := range b.durs {
		duration += int64(d)
	}
	if duration > int64(^uint32(0)) {
		return media.NewError(media.KindOverflow, 0, 0, -1, "duration %d overflow", duration)
	}
	var berr error
	ftyp := mustMarshal(&mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}}}, &berr)
	matrix := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	mvhd := mustMarshal(&mp4.Mvhd{Timescale: timescale, DurationV0: uint32(duration),
		Rate: 0x10000, Volume: 0x100, Matrix: matrix, NextTrackID: 2}, &berr)
	tkhd := mustMarshal(&mp4.Tkhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID: 1, DurationV0: uint32(duration), Matrix: matrix}, &berr)
	mdhd := mustMarshal(&mp4.Mdhd{Timescale: timescale, DurationV0: uint32(duration),
		Language: [3]byte{'u', 'n', 'd'}}, &berr)
	hdlr := mustMarshal(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "avcheck"}, &berr)
	vmhd := mustMarshal(&mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, &berr)
	url := mustMarshal(&mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, &berr)
	dref := mustMarshal(&mp4.Dref{EntryCount: 1}, &berr)
	dinf := container("dinf", append(dref, url...))
	stsd := mustMarshal(&mp4.Stsd{}, &berr)
	sttsB := mustMarshal(&mp4.Stts{EntryCount: uint32(len(stts)), Entries: stts}, &berr)
	stscB := mustMarshal(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(b.count), SampleDescriptionIndex: 1}}}, &berr)
	stszB := mustMarshal(&mp4.Stsz{SampleCount: uint32(b.count), EntrySize: b.sizes}, &berr)
	var stssB []byte
	if len(b.keys) > 0 {
		stssB = mustMarshal(&mp4.Stss{EntryCount: uint32(len(b.keys)), SampleNumber: b.keys}, &berr)
	}
	if berr != nil {
		return media.NewError(media.KindCorrupt, 0, 0, -1, "marshal: %v", berr)
	}
	stcoB := mustMarshal(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, &berr)
	moovLen := 0
	parts := [][]byte{mvhd, tkhd, mdhd, hdlr, vmhd, dinf, stsd, sttsB, stscB, stszB, stcoB}
	if stssB != nil {
		parts = append(parts, stssB)
	}
	for _, p := range parts {
		moovLen += len(p)
	}
	moovLen += 8 * 5
	mdatStart := uint64(len(ftyp)) + uint64(moovLen) + 8
	if mdatStart > uint64(^uint32(0)) {
		return media.NewError(media.KindOverflow, 0, 0, -1, "mdat offset overflow")
	}
	stcoB = mustMarshal(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(mdatStart)}}, &berr)
	stblParts := [][]byte{stsd, sttsB, stscB, stszB, stcoB}
	if stssB != nil {
		stblParts = append(stblParts, stssB)
	}
	stbl := container("stbl", stblParts...)
	minf := container("minf", vmhd, dinf, stbl)
	mdia := container("mdia", mdhd, hdlr, minf)
	trak := container("trak", tkhd, mdia)
	moov := container("moov", mvhd, trak)
	if len(moov) != moovLen {
		return media.NewError(media.KindState, 0, 0, -1, "moov size drift %d != %d", len(moov), moovLen)
	}
	out, err := os.Create(b.path)
	if err != nil {
		return err
	}
	if _, err := out.Write(ftyp); err != nil {
		out.Close()
		return err
	}
	if _, err := out.Write(moov); err != nil {
		out.Close()
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(8+total))
	copy(hdr[4:], "mdat")
	if _, err := out.Write(hdr[:]); err != nil {
		out.Close()
		return err
	}
	src, err := os.Open(b.tmpPath)
	if err != nil {
		out.Close()
		return err
	}
	n, cerr := copyN(out, src, total)
	src.Close()
	if cerr != nil {
		out.Close()
		return cerr
	}
	if uint64(n) != total {
		out.Close()
		return media.NewError(media.KindTruncated, 0, 0, int64(n),
			"payload copied %d of %d bytes", n, total)
	}
	return out.Close()
}

// Abort closes the temp payload file and removes it without writing an MP4.
func (b *MP4Builder) Abort() {
	if b == nil {
		return
	}
	if b.data != nil {
		b.data.Close()
	}
	os.Remove(b.tmpPath)
}
