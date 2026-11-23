// Package fixtures builds every sample media file and manifest used by the
// demo and tests.  Nothing is read from disk except files this package wrote:
// the byte streams are generated deterministically from a seed.
package fixtures

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// writeOpts controls deliberately introduced damage so recovery paths can be
// exercised without external tools.
type writeOpts struct {
	// truncateAtBytes, when positive, cuts the final file at that length.
	truncateAtBytes int64
	// truncateFraction, when in (0,1), cuts the final file at that fraction of
	// its length.  Combined with movieBeforeMedia it reproduces a transfer
	// interrupted mid-mdat with the index still readable at the front.
	truncateFraction float64
	// omitChunkOffsets removes the stco box, forcing index rebuild.
	omitChunkOffsets bool
	// corruptChunkIndex, when >= 0, overlaps that chunk with its predecessor.
	corruptChunkIndex int
	// corruptSampleIndex, when >= 0, rewrites that sample size to a value that
	// is not a whole PCM frame so its boundary cannot be confirmed.
	corruptSampleIndex int
	// movieBeforeMedia places moov ahead of mdat (fast-start layout).
	movieBeforeMedia bool
}

func defaultOpts() writeOpts {
	return writeOpts{corruptChunkIndex: -1, corruptSampleIndex: -1}
}

// chunking describes how samples are grouped into chunks.
type chunking struct {
	samplesPerChunk int
	framesPerSample int
}

// builder wraps go-mp4's low-level writer with a nesting stack.
type builder struct {
	w   *mp4.Writer
	err error
}

func newBuilder(w io.WriteSeeker) *builder { return &builder{w: mp4.NewWriter(w)} }

func (b *builder) start(t mp4.BoxType) {
	if b.err != nil {
		return
	}
	_, err := b.w.StartBox(&mp4.BoxInfo{Type: t})
	b.err = err
}

func (b *builder) end() {
	if b.err != nil {
		return
	}
	_, b.err = b.w.EndBox()
}

// payload writes a box whose fields are marshalled from v.
func (b *builder) payload(v mp4.IImmutableBox) {
	if b.err != nil {
		return
	}
	_, b.err = mp4.Marshal(b.w, v, mp4.Context{})
}

// raw writes n bytes of payload data.
func (b *builder) raw(p []byte) {
	if b.err != nil {
		return
	}
	_, b.err = b.w.Write(p)
}

// box writes a self-contained box.
func (b *builder) box(t mp4.BoxType, v mp4.IImmutableBox) {
	b.start(t)
	b.payload(v)
	b.end()
}

// container writes a box whose body is produced by fn.
func (b *builder) container(t mp4.BoxType, fn func()) {
	b.start(t)
	fn()
	b.end()
}

func (b *builder) seek(off int64) {
	if b.err != nil {
		return
	}
	_, b.err = b.w.Seek(off, io.SeekStart)
}

func (b *builder) pos() int64 {
	if b.err != nil {
		return 0
	}
	p, err := b.w.Seek(0, io.SeekCurrent)
	if err != nil {
		b.err = err
		return 0
	}
	return p
}

// audioFileSpec is the full description of one generated audio MP4.
type audioFileSpec struct {
	movieTimescale uint32
	mediaTimescale uint32
	trackID        uint32
	sampleRate     int
	channels       int
	bitDepth       int
	// sampleFrames is how many PCM frames each MP4 sample holds.
	sampleFrames    int
	samplesPerChunk int
	// pcm is the raw interleaved s16le payload, already at sampleRate.
	pcm []byte
	// edits are expressed in the movie timescale; mediaTime is in media ticks.
	edits []editSpec
	opts  writeOpts
	// variableSizes stores an explicit stsz entry per sample instead of a
	// constant sample size, exercising the size-table reader.
	variableSizes bool
	// syncEvery marks every Nth sample as a sync sample in stss; 0 omits stss.
	syncEvery int
}

type editSpec struct {
	mediaTime int64
	duration  int64
	empty     bool
}

func (s audioFileSpec) blockAlign() int  { return s.channels * (s.bitDepth / 8) }
func (s audioFileSpec) sampleBytes() int { return s.sampleFrames * s.blockAlign() }
func (s audioFileSpec) sampleCount() int { return len(s.pcm) / s.sampleBytes() }

// buildAudioBytes renders the MP4 into memory and returns the concrete bytes
// plus the layout metadata a scanner would otherwise have to rediscover.
func buildAudioBytes(s audioFileSpec) ([]byte, error) {
	if s.opts.corruptChunkIndex < 0 && s.opts.corruptSampleIndex < 0 {
		s.opts.corruptChunkIndex = -1
	}
	perChunk := s.samplesPerChunk
	n := s.sampleCount()
	if n == 0 {
		return nil, fmt.Errorf("fixtures: empty PCM payload")
	}
	chunks := (n + perChunk - 1) / perChunk

	// Sample sizes: constant unless variable or corruption forces a table.
	sizes := make([]uint32, n)
	for i := range sizes {
		sizes[i] = uint32(s.sampleBytes())
	}
	if s.opts.corruptSampleIndex >= 0 && s.opts.corruptSampleIndex < n {
		// 4095 is not a multiple of the 4-byte stereo frame, so the sample
		// boundary and every following sample in its chunk are unconfirmable.
		sizes[s.opts.corruptSampleIndex] = 4095
	}

	// moovSize measures the exact serialized size using a real random-access
	// buffer, because EndBox patches its own header in place.
	moovSize := func(mdatOffset uint64, chunkOffsets []uint32) uint64 {
		buf := &byteBuffer{}
		b := newBuilder(buf)
		writeMoov(b, s, sizes, chunkOffsets, mdatOffset, chunks)
		if b.err != nil {
			return ^uint64(0)
		}
		return uint64(buf.Len())
	}

	// Two-pass layout: build ftyp and mdat position first, then finalize
	// offsets now that moov's exact size is known (all offsets are 32-bit so
	// the size is stable regardless of their values).
	const mdatHeader = 8
	var ftypSize uint64
	chunkOffsets := make([]uint32, chunks)
	if s.opts.movieBeforeMedia {
		// moov precedes mdat. Compute its size with zero offsets.
		estimate := moovSize(0, chunkOffsets)
		if estimate == ^uint64(0) {
			return nil, fmt.Errorf("fixtures: moov sizing failed")
		}
		ftypSize = uint64(ftypLen())
		payloadStart := ftypSize + estimate + mdatHeader
		cursor := payloadStart
		for c := 0; c < chunks; c++ {
			chunkOffsets[c] = uint32(cursor)
			cursor += uint64(chunkPayloadLen(sizes, c*perChunk, (c+1)*perChunk))
		}
	} else {
		ftypSize = uint64(ftypLen())
		payloadStart := ftypSize + mdatHeader
		cursor := payloadStart
		for c := 0; c < chunks; c++ {
			chunkOffsets[c] = uint32(cursor)
			cursor += uint64(chunkPayloadLen(sizes, c*perChunk, (c+1)*perChunk))
		}
	}

	var out byteBuffer
	b := newBuilder(&out)
	b.start(mp4.BoxTypeFtyp())
	b.payload(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 'p', 'c', 'm'}},
		},
	})
	b.end()
	if b.err != nil {
		return nil, b.err
	}
	writeMoovAt := func() {
		writeMoov(b, s, sizes, chunkOffsets, 0, chunks)
	}
	if s.opts.movieBeforeMedia {
		writeMoovAt()
	}
	b.start(mp4.BoxTypeMdat())
	b.raw(s.pcm)
	b.end()
	if !s.opts.movieBeforeMedia {
		writeMoovAt()
	}
	if b.err != nil {
		return nil, b.err
	}
	raw := out.Bytes()
	if s.opts.truncateFraction > 0 && s.opts.movieBeforeMedia {
		keep := int(float64(len(raw)) * s.opts.truncateFraction)
		if keep > 0 && keep < len(raw) {
			raw = raw[:keep:keep]
		}
	}
	if s.opts.truncateAtBytes > 0 && s.opts.truncateAtBytes < int64(len(raw)) {
		raw = raw[:s.opts.truncateAtBytes]
	}
	return raw, nil
}

func chunkPayloadLen(sizes []uint32, lo, hi int) int {
	if hi > len(sizes) {
		hi = len(sizes)
	}
	var sum int
	for i := lo; i < hi; i++ {
		sum += int(sizes[i])
	}
	return sum
}

func writeMoov(b *builder, s audioFileSpec, sizes []uint32, chunkOffsets []uint32, mdatOffset uint64, chunks int) {
	perChunk := s.samplesPerChunk
	n := len(sizes)
	mediaDuration := uint32(float64(n*s.sampleFrames) / float64(s.sampleRate) * float64(s.mediaTimescale))
	var movieDuration uint32
	for _, e := range s.edits {
		movieDuration += uint32(e.duration)
	}

	b.container(mp4.BoxTypeMoov(), func() {
		mvhd := &mp4.Mvhd{
			Timescale:   s.movieTimescale,
			DurationV0:  movieDuration,
			Rate:        0x00010000,
			Volume:      0x0100,
			Matrix:      identityMatrix(),
			NextTrackID: s.trackID + 1,
		}
		b.box(mp4.BoxTypeMvhd(), mvhd)

		b.container(mp4.BoxTypeTrak(), func() {
			tkhd := &mp4.Tkhd{
				TrackID:    s.trackID,
				DurationV0: movieDuration,
				Volume:     0x0100,
				Matrix:     identityMatrix(),
				Width:      0,
				Height:     0,
			}
			tkhd.SetFlags(0x000003) // enabled + in movie
			b.box(mp4.BoxTypeTkhd(), tkhd)

			if len(s.edits) > 0 {
				b.container(mp4.BoxTypeEdts(), func() {
					entries := make([]mp4.ElstEntry, len(s.edits))
					for i, e := range s.edits {
						mt := e.mediaTime
						if e.empty {
							mt = -1
						}
						entries[i] = mp4.ElstEntry{
							SegmentDurationV0: uint32(e.duration),
							MediaTimeV0:       int32(mt),
							MediaRateInteger:  1,
						}
					}
					b.box(mp4.BoxTypeElst(), &mp4.Elst{EntryCount: uint32(len(entries)), Entries: entries})
				})
			}

			b.container(mp4.BoxTypeMdia(), func() {
				b.box(mp4.BoxTypeMdhd(), &mp4.Mdhd{
					Timescale:  s.mediaTimescale,
					DurationV0: mediaDuration,
					Language:   [3]byte{'u', 'n', 'd'},
				})
				hdlr := &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}
				b.box(mp4.BoxTypeHdlr(), hdlr)

				b.container(mp4.BoxTypeMinf(), func() {
					b.box(mp4.BoxTypeSmhd(), &mp4.Smhd{})
					b.container(mp4.BoxTypeDinf(), func() {
						b.container(mp4.BoxTypeDref(), func() {
							// Dref's EntryCount is a plain field; Url is a full box.
							b.payload(&mp4.Dref{EntryCount: 1})
							url := &mp4.Url{}
							url.SetFlags(0x000001)
							b.box(mp4.BoxTypeUrl(), url)
						})
					})
					b.container(mp4.BoxTypeStbl(), func() {
						b.container(mp4.BoxTypeStsd(), func() {
							b.payload(&mp4.Stsd{EntryCount: 1})
							writesAudioSampleEntry(b, s)
						})

						// stts: one run of uniform sample deltas plus a
						// possible shorter tail run.
						baseDelta := uint32(s.sampleFrames)
						b.box(mp4.BoxTypeStts(), &mp4.Stts{
							EntryCount: 1,
							Entries:    []mp4.SttsEntry{{SampleCount: uint32(n), SampleDelta: baseDelta}},
						})

						entries := make([]mp4.StscEntry, 0, chunks)
						for c := 0; c < chunks; c++ {
							count := perChunk
							if c == chunks-1 && n%perChunk != 0 {
								count = n % perChunk
							}
							entries = append(entries, mp4.StscEntry{
								FirstChunk:             uint32(c + 1),
								SamplesPerChunk:        uint32(count),
								SampleDescriptionIndex: 1,
							})
						}
						b.box(mp4.BoxTypeStsc(), &mp4.Stsc{EntryCount: uint32(len(entries)), Entries: entries})

						if s.variableSizes || hasCorruptSize(s) {
							b.box(mp4.BoxTypeStsz(), &mp4.Stsz{SampleCount: uint32(n), EntrySize: sizes})
						} else {
							b.box(mp4.BoxTypeStsz(), &mp4.Stsz{SampleSize: uint32(s.sampleBytes()), SampleCount: uint32(n)})
						}

						if s.syncEvery > 0 {
							var syncs []uint32
							for i := 0; i < n; i += s.syncEvery {
								syncs = append(syncs, uint32(i+1))
							}
							b.box(mp4.BoxTypeStss(), &mp4.Stss{EntryCount: uint32(len(syncs)), SampleNumber: syncs})
						}

						if !s.opts.omitChunkOffsets {
							offs := make([]uint32, len(chunkOffsets))
							copy(offs, chunkOffsets)
							if s.opts.corruptChunkIndex >= 0 && s.opts.corruptChunkIndex < len(offs) && s.opts.corruptChunkIndex > 0 {
								// Overlap the damaged chunk with its predecessor;
								// its extent can no longer be trusted.
								offs[s.opts.corruptChunkIndex] = offs[s.opts.corruptChunkIndex-1] + uint32(s.blockAlign())
							}
							b.box(mp4.BoxTypeStco(), &mp4.Stco{EntryCount: uint32(len(offs)), ChunkOffset: offs})
						}
					})
				})
			})
		})
	})
}

func hasCorruptSize(s audioFileSpec) bool {
	return s.opts.corruptSampleIndex >= 0 && s.opts.corruptSampleIndex < s.sampleCount()
}

func writesAudioSampleEntry(b *builder, s audioFileSpec) {
	ase := &mp4.AudioSampleEntry{
		SampleEntry:  mp4.SampleEntry{DataReferenceIndex: 1},
		ChannelCount: uint16(s.channels),
		SampleSize:   uint16(s.bitDepth),
		SampleRate:   uint32(s.sampleRate) << 16,
	}
	ase.SetType(mp4.BoxTypeIpcm())
	b.start(mp4.BoxTypeIpcm())
	b.payload(ase)
	pcmC := &mp4.PcmC{FormatFlags: 1, PCMSampleSize: uint8(s.bitDepth)} // 1 = little-endian
	b.box(mp4.BoxTypePcmC(), pcmC)
	b.end()
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func ftypLen() int {
	buf := &byteBuffer{}
	b := newBuilder(buf)
	b.start(mp4.BoxTypeFtyp())
	b.payload(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 'p', 'c', 'm'}},
		},
	})
	b.end()
	return buf.Len()
}
