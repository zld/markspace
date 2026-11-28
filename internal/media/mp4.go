package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
)

// NamedHash returns the lowercase SHA-256 hex digest of b. It lives here so
// fixture and runner code share one hashing implementation.
func NamedHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Track is one track discovered in an MP4 file. Timing is kept in the track's
// own timescale exactly as stored; conversion to nanoseconds uses Timebase.
type Track struct {
	ID            uint32
	Handler       string
	Timescale     uint32
	DurationTicks uint64
	Width         int
	Height        int
	EntryType     string
	SampleSizes   []uint32
	SttsEntries   []mp4.SttsEntry
	StscEntries   []mp4.StscEntry
	ChunkOffsets  []uint64
	SampleOffsets []int64

	lumaFrames [][]byte
}

// DurationNanos converts the track duration with no floating point.
func (t *Track) DurationNanos() (int64, error) {
	tb, err := NewTimebase(t.Timescale, "track")
	if err != nil {
		return 0, err
	}
	var ticks int64
	for _, e := range t.SttsEntries {
		n := int64(e.SampleCount) * int64(e.SampleDelta)
		if n < 0 || ticks > (1<<62)-n {
			return 0, New(StageParse, "mp4", "track duration overflow")
		}
		ticks += n
	}
	return tb.ToNanos(ticks)
}

// SampleBytes is the sum of all sample sizes in the track.
func (t *Track) SampleBytes() int64 {
	var n int64
	for _, s := range t.SampleSizes {
		n += int64(s)
	}
	return n
}

// ByteRange is an absolute file range covered by an mdat payload.
type ByteRange struct {
	Start int64
	End   int64
}

// Fragment is one movie fragment (moof) with its timed samples.
type Fragment struct {
	TrackID       uint32
	Sequence      uint32
	BaseDecode    uint64
	DurationTicks uint64
	SampleBytes   int64
	SampleOffsets []int64
}

// FileProbe is the validated structure of one MP4 file.
type FileProbe struct {
	MajorBrand string
	Tracks     []*Track
	Fragments  []Fragment
	Mdat       []ByteRange
	Size       int64
	// Luma holds extracted uncompressed luma planes for the video track when
	// requested. Empty when the codec is compressed or extraction was off.
	Luma [][]byte
	// PrivateRTP is the payload of a fixture-private "rtp0" box when present.
	PrivateRTP []byte
}

// DurationNanos reports the longest track timeline: stbl sample durations
// when a moov sample table is present, otherwise the sum of fragment
// durations. Zero means the duration could not be established.
func (p *FileProbe) DurationNanos() int64 {
	var best int64
	for _, t := range p.Tracks {
		if n, err := t.DurationNanos(); err == nil && n > best {
			best = n
		}
	}
	if best == 0 {
		byTrack := make(map[uint32]uint64)
		for _, f := range p.Fragments {
			byTrack[f.TrackID] += f.DurationTicks
		}
		for trackID, ticks := range byTrack {
			var ts uint32
			for _, t := range p.Tracks {
				if t.ID == trackID {
					ts = t.Timescale
				}
			}
			if ts == 0 {
				continue
			}
			tb := Timebase{Timescale: ts}
			if n, err := tb.ToNanos(int64(ticks)); err == nil && n > best {
				best = n
			}
		}
	}
	return best
}

// Video returns the first video track, if any.
func (p *FileProbe) Video() *Track {
	for _, t := range p.Tracks {
		if t.Handler == "vide" {
			return t
		}
	}
	return nil
}

// ProbeOptions bounds probing.
type ProbeOptions struct {
	// ExtractLuma reads uncompressed ("raw ") luma planes from the video track.
	ExtractLuma bool
	// MaxLumaBytes caps total extracted plane bytes.
	MaxLumaBytes int64
	// MaxSamples caps sample count considered for offset expansion.
	MaxSamples int
}

var containerBoxes = map[mp4.BoxType]bool{
	mp4.BoxTypeMoov(): true,
	mp4.BoxTypeTrak(): true,
	mp4.BoxTypeMdia(): true,
	mp4.BoxTypeMinf(): true,
	mp4.BoxTypeStbl(): true,
	mp4.BoxTypeMoof(): true,
	mp4.BoxTypeTraf(): true,
	mp4.BoxTypeMvex(): true,
	mp4.BoxTypeDinf(): true,
	mp4.BoxTypeEdts(): true,
}

// expandableBox reports whether a container's children should be walked.
func expandableBox(t mp4.BoxType) bool {
	return containerBoxes[t] || t == mp4.StrToBoxType("alab")
}

// ProbeFile validates MP4/fMP4 box structure with go-mp4 and extracts timing
// and sample layout. Structural damage is reported with its byte offset.
func ProbeFile(path string, opts ProbeOptions) (*FileProbe, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Wrap(StageParse, "mp4", err, "cannot open media").WithPath(path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, Wrap(StageParse, "mp4", err, "cannot stat media").WithPath(path)
	}
	return probe(f, st.Size(), path, opts)
}

func probe(r io.ReadSeeker, size int64, path string, opts ProbeOptions) (*FileProbe, error) {
	type fragState struct {
		trackID   uint32
		seq       uint32
		base      uint64
		defDur    uint32
		defSize   uint32
		sizes     []uint32
		durs      []uint32
		dataOff   int32
		haveTfhd  bool
		haveTfdt  bool
		moofStart int64
		moofSize  uint64
	}
	var frag *fragState
	if opts.MaxSamples <= 0 {
		opts.MaxSamples = 2_000_000
	}
	if opts.MaxLumaBytes <= 0 {
		opts.MaxLumaBytes = 32 << 20
	}
	p := &FileProbe{Size: size}
	trackByPath := make(map[string]*Track)

	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		bi := h.BoxInfo
		if bi.Size > uint64(size)-bi.Offset || bi.Offset+bi.Size > uint64(size) {
			return nil, New(StageParse, "mp4", "box %s declares end %d beyond file size %d", bi.Type.String(), bi.Offset+bi.Size, size).
				WithPath(path).WithOffset(int64(bi.Offset))
		}
		switch bi.Type {
		case mp4.BoxTypeMoof():
			frag = &fragState{moofStart: int64(bi.Offset), moofSize: bi.Size}
			p.Fragments = append(p.Fragments, Fragment{})
		case mp4.BoxTypeMfhd():
			if frag == nil {
				break
			}
			var box mp4.Mfhd
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid mfhd").WithPath(path).WithOffset(int64(bi.Offset))
			}
			frag.seq = box.SequenceNumber
		case mp4.BoxTypeTfhd():
			if frag == nil {
				break
			}
			var box mp4.Tfhd
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid tfhd").WithPath(path).WithOffset(int64(bi.Offset))
			}
			frag.trackID = box.TrackID
			frag.defDur = box.DefaultSampleDuration
			frag.defSize = box.DefaultSampleSize
			frag.haveTfhd = true
		case mp4.BoxTypeTfdt():
			if frag == nil {
				break
			}
			var box mp4.Tfdt
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid tfdt").WithPath(path).WithOffset(int64(bi.Offset))
			}
			frag.base = box.GetBaseMediaDecodeTime()
			frag.haveTfdt = true
		case mp4.BoxTypeTrun():
			if frag == nil {
				break
			}
			var box mp4.Trun
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid trun").WithPath(path).WithOffset(int64(bi.Offset))
			}
			frag.dataOff = box.DataOffset
			for _, e := range box.Entries {
				sz := e.SampleSize
				if sz == 0 {
					sz = frag.defSize
				}
				d := e.SampleDuration
				if d == 0 {
					d = frag.defDur
				}
				frag.sizes = append(frag.sizes, sz)
				frag.durs = append(frag.durs, d)
			}
		case mp4.BoxTypeFtyp():
			var box mp4.Ftyp
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid ftyp").WithPath(path).WithOffset(int64(bi.Offset))
			}
			p.MajorBrand = trimBrand(box.MajorBrand[:])
		case mp4.BoxTypeMvhd():
			var box mp4.Mvhd
			if err := unmarshalPayload(r, &bi, &box); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid mvhd").WithPath(path).WithOffset(int64(bi.Offset))
			}
		case mp4.BoxTypeTrak():
			t := &Track{}
			p.Tracks = append(p.Tracks, t)
			trackByPath[pathKey(h.Path)] = t
		case mp4.BoxTypeMdhd(), mp4.BoxTypeTkhd(), mp4.BoxTypeHdlr(),
			mp4.BoxTypeStts(), mp4.BoxTypeStsz(), mp4.BoxTypeStco(), mp4.BoxTypeStsc():
			t := trackFor(h.Path, trackByPath)
			if t == nil {
				break
			}
			switch bi.Type {
			case mp4.BoxTypeMdhd():
				var box mp4.Mdhd
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid mdhd").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.Timescale = box.Timescale
				if box.GetVersion() == 0 {
					t.DurationTicks = uint64(box.DurationV0)
				} else {
					t.DurationTicks = box.DurationV1
				}
			case mp4.BoxTypeTkhd():
				var box mp4.Tkhd
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid tkhd").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.ID = box.TrackID
				t.Width = int(box.Width >> 16)
				t.Height = int(box.Height >> 16)
			case mp4.BoxTypeHdlr():
				var box mp4.Hdlr
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid hdlr").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.Handler = trimBrand(box.HandlerType[:])
			case mp4.BoxTypeStts():
				var box mp4.Stts
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid stts").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.SttsEntries = box.Entries
			case mp4.BoxTypeStsz():
				var box mp4.Stsz
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid stsz").WithPath(path).WithOffset(int64(bi.Offset))
				}
				if int(box.SampleCount) > opts.MaxSamples {
					return nil, New(StageParse, "mp4", "sample count %d exceeds limit %d", box.SampleCount, opts.MaxSamples).
						WithPath(path).WithOffset(int64(bi.Offset))
				}
				if box.SampleSize != 0 {
					t.SampleSizes = make([]uint32, box.SampleCount)
					for i := range t.SampleSizes {
						t.SampleSizes[i] = box.SampleSize
					}
				} else {
					t.SampleSizes = box.EntrySize
				}
			case mp4.BoxTypeStco():
				var box mp4.Stco
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid stco").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.ChunkOffsets = make([]uint64, len(box.ChunkOffset))
				for i, o := range box.ChunkOffset {
					t.ChunkOffsets[i] = uint64(o)
				}
			case mp4.BoxTypeStsc():
				var box mp4.Stsc
				if err := unmarshalPayload(r, &bi, &box); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "invalid stsc").WithPath(path).WithOffset(int64(bi.Offset))
				}
				t.StscEntries = box.Entries
			}
		case mp4.BoxTypeStsd():
			t := trackFor(h.Path, trackByPath)
			if t == nil {
				break
			}
			raw := make([]byte, bi.Size-bi.HeaderSize)
			if _, err := bi.SeekToPayload(r); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "seek stsd").WithPath(path).WithOffset(int64(bi.Offset))
			}
			if _, err := io.ReadFull(r, raw); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "truncated stsd").WithPath(path).WithOffset(int64(bi.Offset))
			}
			entry, w, hgt, err := parseStsd(raw)
			if err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid stsd").WithPath(path).WithOffset(int64(bi.Offset))
			}
			t.EntryType = entry
			if t.Width == 0 {
				t.Width = w
			}
			if t.Height == 0 {
				t.Height = hgt
			}
		case mp4.StrToBoxType("rtp0"):
			raw := make([]byte, bi.Size-bi.HeaderSize)
			if _, err := bi.SeekToPayload(r); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "seek rtp0").WithPath(path).WithOffset(int64(bi.Offset))
			}
			if _, err := io.ReadFull(r, raw); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "truncated rtp0").WithPath(path).WithOffset(int64(bi.Offset))
			}
			p.PrivateRTP = raw
			return nil, nil
		case mp4.BoxTypeMdat():
			p.Mdat = append(p.Mdat, ByteRange{Start: int64(bi.Offset + bi.HeaderSize), End: int64(bi.Offset + bi.Size)})
			if frag != nil {
				// Samples default to starting right after the moof.
				dataStart := int64(frag.moofStart) + int64(frag.moofSize)
				if frag.dataOff != 0 {
					dataStart = int64(frag.moofStart) + int64(frag.dataOff)
				}
				var off int64 = dataStart
				var dur uint64
				var bytes int64
				for i := range frag.sizes {
					p.Fragments[len(p.Fragments)-1].SampleOffsets = append(p.Fragments[len(p.Fragments)-1].SampleOffsets, off)
					off += int64(frag.sizes[i])
					dur += uint64(frag.durs[i])
					bytes += int64(frag.sizes[i])
				}
				last := len(p.Fragments) - 1
				p.Fragments[last].TrackID = frag.trackID
				p.Fragments[last].Sequence = frag.seq
				p.Fragments[last].BaseDecode = frag.base
				p.Fragments[last].DurationTicks = dur
				p.Fragments[last].SampleBytes = bytes
			}
			return nil, nil
		}
		if bi.Type == mp4.BoxTypeUdta() {
			raw := make([]byte, bi.Size-bi.HeaderSize)
			if _, err := bi.SeekToPayload(r); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "seek udta").WithPath(path).WithOffset(int64(bi.Offset))
			}
			if _, err := io.ReadFull(r, raw); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "truncated udta").WithPath(path).WithOffset(int64(bi.Offset))
			}
			if idx := bytes.Index(raw, []byte("rtp0")); idx >= 4 {
				// Re-enter the reader at the rtp0 box header so the normal
				// payload handler records it.
				if _, err := r.Seek(int64(bi.Offset+bi.HeaderSize)+int64(idx-4), io.SeekStart); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "seek rtp0").WithPath(path)
				}
				child, cerr := mp4.ReadBoxInfo(r)
				if cerr != nil {
					return nil, Wrap(StageParse, "mp4", cerr, "invalid private rtp0 box").WithPath(path)
				}
				p.PrivateRTP = make([]byte, child.Size-child.HeaderSize)
				if _, err := io.ReadFull(r, p.PrivateRTP); err != nil {
					return nil, Wrap(StageParse, "mp4", err, "truncated rtp0").WithPath(path).WithOffset(int64(child.Offset))
				}
			}
			return nil, nil
		}
		if expandableBox(bi.Type) {
			if _, err := h.Expand(); err != nil {
				return nil, Wrap(StageParse, "mp4", err, "invalid %s children", bi.Type.String()).WithPath(path).WithOffset(int64(bi.Offset))
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if p.MajorBrand == "" {
		return nil, New(StageParse, "mp4", "missing ftyp box").WithPath(path).WithOffset(0)
	}
	if len(p.Tracks) == 0 && len(p.Fragments) == 0 && size > 0 {
		return nil, New(StageParse, "mp4", "missing moov/trak and moof/traf boxes").WithPath(path)
	}
	for _, t := range p.Tracks {
		if t.Timescale == 0 {
			return nil, New(StageParse, "mp4", "zero mdhd timescale").WithPath(path)
		}
	}
	// A declared mdat that runs past EOF means the file was truncated even
	// though the root box loop accepts a short read at the top level.
	for _, m := range p.Mdat {
		if m.End > size {
			return nil, New(StageParse, "mp4", "mdat ends at %d, file size is %d (truncated)", m.End, size).
				WithPath(path).WithOffset(m.Start)
		}
	}
	for _, f := range p.Fragments {
		for i, off := range f.SampleOffsets {
			if i >= len(f.SampleOffsets) {
				break
			}
			if !insideMdat(off, off, p.Mdat) {
				return nil, New(StageParse, "mp4", "fragment sample %d at offset %d lies outside every mdat", i, off).
					WithPath(path).WithOffset(off)
			}
		}
	}
	// Lift fragment sample layout onto the video track for fragmented files
	// whose moov sample table is empty.
	var fragBytes int64
	if v := p.Video(); v != nil && len(v.SampleSizes) == 0 {
		for _, f := range p.Fragments {
			if f.TrackID == v.ID || v.ID == 0 {
				for range f.SampleOffsets {
					v.SampleSizes = append(v.SampleSizes, 0)
				}
				v.SampleOffsets = append(v.SampleOffsets, f.SampleOffsets...)
				// Fragment sample byte counts are additive; keep a running
				// total so per-sample sizes can be inferred below.
				fragBytes += f.SampleBytes
			}
		}
		// Sample sizes are needed for luma extraction. Fragments record the
		// total sample bytes only when the trun sizes were implicit, so use
		// the planar geometry to recover the per-sample size.
		if len(v.SampleSizes) > 0 && v.SampleSizes[0] == 0 {
			per := int64(0)
			if v.Width > 0 && v.Height > 0 {
				per = int64(v.Width) * int64(v.Height)
			}
			if per == 0 && len(v.SampleSizes) > 0 {
				per = fragBytes / int64(len(v.SampleSizes))
			}
			for i := range v.SampleSizes {
				v.SampleSizes[i] = uint32(per)
			}
		}
	}
	// Expand sample offsets for the video track and verify they stay inside
	// an mdat payload.
	if v := p.Video(); v != nil {
		if err := expandSampleOffsets(v, p.Mdat, path); err != nil {
			return nil, err
		}
		if opts.ExtractLuma && v.EntryType == "raw " {
			for i := 0; i < len(v.SampleOffsets); i++ {
				sz := int(v.SampleSizes[i])
				if sz < v.Width*v.Height {
					return nil, New(StageParse, "mp4", "raw sample %d is smaller than its %dx%d plane", i, v.Width, v.Height).
						WithPath(path).WithOffset(v.SampleOffsets[i])
				}
			}
			frames, err := extractLuma(r, v, opts.MaxLumaBytes, path)
			if err != nil {
				return nil, err
			}
			p.Luma = frames
		}
	}
	return p, nil
}

func unmarshalPayload(r io.ReadSeeker, bi *mp4.BoxInfo, dst mp4.IBox) error {
	if _, err := bi.SeekToPayload(r); err != nil {
		return err
	}
	raw := make([]byte, bi.Size-bi.HeaderSize)
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	if _, err := mp4.Unmarshal(bytes.NewReader(raw), uint64(len(raw)), dst, bi.Context); err != nil {
		return err
	}
	return nil
}

func pathKey(path mp4.BoxPath) string {
	var b bytes.Buffer
	for _, t := range path {
		b.Write(t[:])
	}
	return b.String()
}

func trackFor(path mp4.BoxPath, m map[string]*Track) *Track {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == mp4.BoxTypeTrak() {
			return m[pathKey(path[:i+1])]
		}
	}
	return nil
}

func trimBrand(b []byte) string {
	n := bytes.IndexByte(b, 0)
	if n < 0 {
		n = len(b)
	}
	return string(b[:n])
}

// parseStsd extracts the first sample entry type and visual dimensions.
func parseStsd(raw []byte) (string, int, int, error) {
	if len(raw) < 16 {
		return "", 0, 0, fmt.Errorf("stsd too short")
	}
	entrySize := int(binary.BigEndian.Uint32(raw[8:12]))
	entry := string(raw[12:16])
	if entrySize <= 0 || entrySize > len(raw)-8 {
		return "", 0, 0, fmt.Errorf("stsd entry size %d invalid", entrySize)
	}
	var w, h int
	if len(raw) >= 44 && isVisualEntry(entry) {
		w = int(binary.BigEndian.Uint16(raw[40:42]))
		h = int(binary.BigEndian.Uint16(raw[42:44]))
	}
	return entry, w, h, nil
}

func isVisualEntry(entry string) bool {
	switch entry {
	case "raw ", "avc1", "avc3", "hvc1", "hev1", "vp09", "av01":
		return true
	}
	return false
}

func expandSampleOffsets(t *Track, mdats []ByteRange, path string) error {
	if len(t.ChunkOffsets) == 0 {
		return nil
	}
	if len(t.StscEntries) == 0 {
		return New(StageParse, "mp4", "stco present without stsc").WithPath(path)
	}
	var sizes []uint32
	for _, e := range t.StscEntries {
		if e.SamplesPerChunk != 0 {
			sizes = append(sizes, e.SamplesPerChunk)
		}
	}
	sampleIdx := 0
	chunkCount := uint32(len(t.ChunkOffsets))
	for chunk := uint32(0); chunk < chunkCount && sampleIdx < len(t.SampleSizes); chunk++ {
		spc := samplesPerChunkFor(chunk+1, t.StscEntries)
		if spc == 0 {
			return New(StageParse, "mp4", "stsc yields zero samples for chunk %d", chunk+1).WithPath(path)
		}
		off := int64(t.ChunkOffsets[chunk])
		for s := uint32(0); s < spc && sampleIdx < len(t.SampleSizes); s++ {
			sz := int64(t.SampleSizes[sampleIdx])
			if !insideMdat(off, off+sz, mdats) {
				return New(StageParse, "mp4", "sample %d at offset %d lies outside every mdat", sampleIdx, off).
					WithPath(path).WithOffset(off)
			}
			t.SampleOffsets = append(t.SampleOffsets, off)
			off += sz
			sampleIdx++
		}
	}
	if sampleIdx != len(t.SampleSizes) {
		return New(StageParse, "mp4", "chunk table maps %d of %d samples", sampleIdx, len(t.SampleSizes)).WithPath(path)
	}
	return nil
}

func samplesPerChunkFor(chunk uint32, entries []mp4.StscEntry) uint32 {
	var spc uint32
	for _, e := range entries {
		if e.FirstChunk <= chunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

func insideMdat(start, end int64, mdats []ByteRange) bool {
	for _, m := range mdats {
		if start >= m.Start && end <= m.End {
			return true
		}
	}
	return false
}

func extractLuma(r io.ReadSeeker, t *Track, maxBytes int64, path string) ([][]byte, error) {
	plane := int64(t.Width) * int64(t.Height)
	if plane <= 0 {
		return nil, New(StageParse, "mp4", "raw video track has no dimensions").WithPath(path)
	}
	var total int64
	frames := make([][]byte, 0, len(t.SampleOffsets))
	for i, off := range t.SampleOffsets {
		if total+plane > maxBytes {
			return nil, New(StageParse, "mp4", "luma extraction exceeds %d byte limit", maxBytes).WithPath(path)
		}
		buf := make([]byte, plane)
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return nil, Wrap(StageParse, "mp4", err, "seek sample %d", i).WithPath(path).WithOffset(off)
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, Wrap(StageParse, "mp4", err, "read sample %d", i).WithPath(path).WithOffset(off)
		}
		frames = append(frames, buf)
		total += plane
	}
	return frames, nil
}

// PlaneSizeHint returns the per-sample size implied by a fragment layout.
func PlaneSizeHint(samples int, bytes int64) int64 {
	if samples <= 0 {
		return 0
	}
	return bytes / int64(samples)
}

// Name returns the canonical fixture segment file name for an index, using
// the same four-digit decimal format as the generator.
func Name(index int) string {
	return fmt.Sprintf("normal-%04d.m4s", index)
}
