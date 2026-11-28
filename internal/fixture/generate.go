// Package fixture builds the bounded, deterministic regression inputs used by
// every subsystem. Everything is generated locally: no network, no devices,
// no real codec content. The generated MP4 is a minimal valid ISOBMFF file
// whose sample layout is fully known, including byte offsets of table entries
// so corruption tests can assert exact bad_offset values.
package fixture

import (
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"

	"rtptrace/internal/fault"
)

// TrackSpec describes one synthetic track.
type TrackSpec struct {
	TrackID         uint32
	Timescale       uint32
	SampleCount     int
	SampleSize      uint32 // uniform size, 0 means use the default (24 bytes)
	SamplesPerChunk int
	Delta           uint32 // stts sample delta in timescale units
	SSRC            uint32
	PayloadType     uint8
}

// Layout records byte positions tests need for corruption assertions.
type Layout struct {
	FtypOffset       int64
	MdatPayloadStart int64
	MoovOffset       int64
	StcoEntryOffset  [][]int64 // [track][chunk] absolute file offsets
	StssEntryOffset  [][]int64 // [track][entry] absolute file offsets
}

// MP4Fixture is a generated container plus its known layout.
type MP4Fixture struct {
	Path   string
	Size   int64
	Layout *Layout
	Specs  []TrackSpec
}

const defaultSampleSize = uint32(24)

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func box(typ string, payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	out = append(out, be32(uint32(8+len(payload)))...)
	out = append(out, typ...)
	return append(out, payload...)
}

func fullBox(ver uint8, flags uint32, body []byte) []byte {
	p := make([]byte, 0, 4+len(body))
	p = append(p, ver, byte(flags>>16), byte(flags>>8), byte(flags))
	return append(p, body...)
}

func (s TrackSpec) sampleSize() uint32 {
	if s.SampleSize == 0 {
		return defaultSampleSize
	}
	return s.SampleSize
}

func (s TrackSpec) perChunk() int {
	if s.SamplesPerChunk <= 0 {
		return s.SampleCount
	}
	return s.SamplesPerChunk
}

// BuildMP4 writes a deterministic MP4 containing ftyp, mdat and moov boxes.
func BuildMP4(dir string, seed int64, specs []TrackSpec) (*MP4Fixture, error) {
	if len(specs) == 0 {
		return nil, fault.New(fault.KindInternal, "specs", fault.NoOffset, "no tracks requested")
	}
	rng := rand.New(rand.NewSource(seed))

	ftyp := box("ftyp", append(append(be32(0x69736f6d), be32(0)...), []byte("isomiso2")...))
	var file []byte
	file = append(file, ftyp...)

	// mdat: each track writes contiguous deterministic samples grouped into
	// chunks; chunk offsets are captured before serialization of moov.
	mdatPayloadStart := len(file) + 8
	var mdat []byte
	type chunkInfo struct{ offsets []uint32 }
	trackChunks := make([]chunkInfo, len(specs))
	totalSamples := 0
	for ti, sp := range specs {
		size := sp.sampleSize()
		written := 0
		for written < sp.SampleCount {
			off := uint32(mdatPayloadStart + len(mdat))
			trackChunks[ti].offsets = append(trackChunks[ti].offsets, off)
			n := sp.perChunk()
			if written+n > sp.SampleCount {
				n = sp.SampleCount - written
			}
			for k := 0; k < n; k++ {
				sample := make([]byte, size)
				if _, err := rng.Read(sample); err != nil {
					return nil, fault.Newf(fault.KindInternal, "mdat", fault.NoOffset, "rng: %v", err)
				}
				mdat = append(mdat, sample...)
				written++
				totalSamples++
			}
		}
	}
	file = append(file, box("mdat", mdat)...)
	moovOffset := len(file)

	mvhdBody := append(be32(0), be32(0)...)
	mvhdBody = append(mvhdBody, be32(1000)...)
	mvhdBody = append(mvhdBody, be32(uint32(totalSamples))...)
	mvhd := box("mvhd", fullBox(0, 0, mvhdBody))
	var traks []byte
	for ti, sp := range specs {
		traks = append(traks, buildTrak(sp, trackChunks[ti].offsets)...)
	}
	file = append(file, box("moov", append(mvhd, traks...))...)

	// Scan the finalized buffer so nested table entry offsets are exact.
	layout, err := scanLayout(file)
	if err != nil {
		return nil, err
	}
	layout.FtypOffset = 0
	layout.MdatPayloadStart = int64(mdatPayloadStart)
	layout.MoovOffset = int64(moovOffset)

	path := filepath.Join(dir, "trace.mp4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.Newf(fault.KindInternal, "dir", fault.NoOffset, "%v", err)
	}
	if err := os.WriteFile(path, file, 0o644); err != nil {
		return nil, fault.Newf(fault.KindInternal, "file", fault.NoOffset, "%v", err)
	}
	return &MP4Fixture{
		Path:   path,
		Size:   int64(len(file)),
		Layout: layout,
		Specs:  specs,
	}, nil
}

func buildTrak(sp TrackSpec, chunkOffsets []uint32) []byte {
	duration := uint32(sp.SampleCount) * sp.Delta

	tkhdBody := make([]byte, 80)
	binary.BigEndian.PutUint32(tkhdBody[4:8], sp.TrackID)
	binary.BigEndian.PutUint32(tkhdBody[16:20], duration)
	tkhd := box("tkhd", fullBox(0, 7, tkhdBody))

	mdhdBody := append(append(be32(0), be32(0)...), be32(sp.Timescale)...)
	mdhdBody = append(mdhdBody, be32(duration)...)
	mdhdBody = append(mdhdBody, be16(0x55C4)...)
	mdhdBody = append(mdhdBody, be16(0)...)
	mdhd := box("mdhd", fullBox(0, 0, mdhdBody))
	hdlr := box("hdlr", fullBox(0, 0, append(append(be32(0),
		[]byte("vide")...), make([]byte, 12)...)))

	stts := box("stts", fullBox(0, 0, append(be32(1),
		append(be32(uint32(sp.SampleCount)), be32(sp.Delta)...)...)))
	stsc := box("stsc", fullBox(0, 0, append(be32(1),
		append(be32(1), append(be32(uint32(sp.perChunk())), be32(1)...)...)...)))
	stszBody := append(be32(0), be32(uint32(sp.SampleCount))...)
	for i := 0; i < sp.SampleCount; i++ {
		stszBody = append(stszBody, be32(sp.sampleSize())...)
	}
	stsz := box("stsz", fullBox(0, 0, stszBody))
	stss := box("stss", fullBox(0, 0, append(be32(1), be32(1)...)))

	var stcoBody []byte
	for _, off := range chunkOffsets {
		stcoBody = append(stcoBody, be32(off)...)
	}
	stco := box("stco", fullBox(0, 0, append(be32(uint32(len(chunkOffsets))), stcoBody...)))

	stbl := box("stbl", append(append(append(append(stts, stsc...), stsz...), stss...), stco...))
	minf := box("minf", stbl)
	mdia := box("mdia", append(append(mdhd, hdlr...), minf...))
	return box("trak", append(tkhd, mdia...))
}

// scanLayout walks the finalized file recording stco/stss entry offsets per
// trak in walk order, which matches container.ScanFile track ordering.
func scanLayout(file []byte) (*Layout, error) {
	layout := &Layout{}
	var walk func(start, end int) error
	walk = func(start, end int) error {
		pos := start
		for pos+8 <= end {
			size := int(binary.BigEndian.Uint32(file[pos:]))
			if size < 8 || pos+size > end {
				return fault.New(fault.KindInternal, "layout", int64(pos), "bad box while scanning layout")
			}
			typ := string(file[pos+4 : pos+8])
			payload := pos + 8
			switch typ {
			case "trak":
				before := len(layout.StcoEntryOffset)
				if err := walk(payload, pos+size); err != nil {
					return err
				}
				if len(layout.StcoEntryOffset) == before {
					layout.StcoEntryOffset = append(layout.StcoEntryOffset, nil)
					layout.StssEntryOffset = append(layout.StssEntryOffset, nil)
				}
			case "stco":
				entryStart := int64(payload + 4 + 4) // fullbox + entry_count
				count := binary.BigEndian.Uint32(file[payload+4:])
				entries := make([]int64, count)
				for i := uint32(0); i < count; i++ {
					entries[i] = entryStart + int64(i)*4
				}
				layout.StcoEntryOffset = append(layout.StcoEntryOffset, entries)
			case "stss":
				entryStart := int64(payload + 4 + 4)
				count := binary.BigEndian.Uint32(file[payload+4:])
				entries := make([]int64, count)
				for i := uint32(0); i < count; i++ {
					entries[i] = entryStart + int64(i)*4
				}
				layout.StssEntryOffset = append(layout.StssEntryOffset, entries)
			default:
				if isContainerType(typ) {
					if err := walk(payload, pos+size); err != nil {
						return err
					}
				}
			}
			pos += size
		}
		return nil
	}
	if err := walk(0, len(file)); err != nil {
		return nil, err
	}
	return layout, nil
}

func isContainerType(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "udta":
		return true
	}
	return false
}

// MutateChunkOffset overwrites one stco entry and returns its file offset.
func MutateChunkOffset(f *MP4Fixture, track, entry int, value uint32) (int64, error) {
	if track >= len(f.Layout.StcoEntryOffset) || entry >= len(f.Layout.StcoEntryOffset[track]) {
		return 0, fault.New(fault.KindInternal, "stco", fault.NoOffset, "entry out of layout")
	}
	off := f.Layout.StcoEntryOffset[track][entry]
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return 0, fault.Newf(fault.KindInternal, "file", off, "%v", err)
	}
	binary.BigEndian.PutUint32(data[int(off):], value)
	if err := os.WriteFile(f.Path, data, 0o644); err != nil {
		return 0, fault.Newf(fault.KindInternal, "file", off, "%v", err)
	}
	return off, nil
}

// TruncateCopy writes a copy of src shortened to at bytes at dst.
func TruncateCopy(src, dst string, at int64) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fault.Newf(fault.KindInternal, "file", fault.NoOffset, "%v", err)
	}
	if at < 0 || at > int64(len(data)) {
		return fault.New(fault.KindInternal, "at", at, "truncation point outside file")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fault.Newf(fault.KindInternal, "dir", fault.NoOffset, "%v", err)
	}
	return os.WriteFile(dst, data[:at], 0o644)
}

// TruncatePointInTable returns an offset a couple of bytes into the first
// track's stco payload. The table's declared size then runs past EOF, so the
// scanner reports a byte-accurate bad_offset within a few bytes of the cut.
func (f *MP4Fixture) TruncatePointInTable() int64 {
	entries := f.Layout.StcoEntryOffset[0]
	firstEntry := entries[0]
	// stco payload starts 8 bytes (full box prefix + entry count) before the
	// first entry; cut 2 bytes into that payload.
	return firstEntry - 8 + 2
}

// MutateSampleNumber overwrites one stss entry with an out-of-range number.
func MutateSampleNumber(f *MP4Fixture, track, entry int, value uint32) (int64, error) {
	if track >= len(f.Layout.StssEntryOffset) || entry >= len(f.Layout.StssEntryOffset[track]) {
		return 0, fault.New(fault.KindInternal, "stss", fault.NoOffset, "entry out of layout")
	}
	off := f.Layout.StssEntryOffset[track][entry]
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return 0, fault.Newf(fault.KindInternal, "file", off, "%v", err)
	}
	binary.BigEndian.PutUint32(data[int(off):], value)
	if err := os.WriteFile(f.Path, data, 0o644); err != nil {
		return 0, fault.Newf(fault.KindInternal, "file", off, "%v", err)
	}
	return off, nil
}
