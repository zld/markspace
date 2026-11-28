package track

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/abema/go-mp4"

	"mediaregress/internal/media"
)

// ParseMP4 reads track metadata from an ISO-BMFF file. It never rewrites raw
// values: rotation, crop and color descriptions keep both their encoded form
// and the byte offset they were read from. A track whose crop is impossible is
// rejected, not corrected.
func ParseMP4(path string) (*Input, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "open input", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "stat input", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "rewind input", err)
	}
	digest, err := media.NewDigest(f)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "digest input", err)
	}
	in, err := parseMP4(f, fi.Size())
	if in == nil {
		return nil, err
	}
	in.Path = path
	in.Size = fi.Size()
	in.Digest = string(digest)
	if err != nil {
		return in, err
	}
	return in, nil
}

func parseMP4(r io.ReaderAt, size int64) (*Input, error) {
	tree := &boxTree{r: r, fileSize: size}
	roots, treeErr := tree.parse(0, size)
	in := &Input{}
	if treeErr != nil {
		in.Errors = append(in.Errors, treeErr)
	}
	var moov *boxNode
	for _, n := range roots {
		if n.Hdr.Type == "moov" {
			moov = n
			break
		}
	}
	if moov == nil {
		err := media.NewError(media.ClassCorruptInput, media.StageParse, "", 0, "moov box not found")
		in.Errors = append(in.Errors, err)
		return in, err
	}
	sk := &seeker{r: r}
	// Pass 1: read the handler type of every trak so track IDs follow the
	// container's ordering (first vide -> v0, first soun -> a0) even when the
	// audio trak appears first in the file.
	traks := findAll(moov, "trak")
	handlerKinds := make([]Kind, len(traks))
	for i, trak := range traks {
		handlerKinds[i] = handlerKind(sk, trak)
	}
	seen := map[Kind]int{}
	for i, trak := range traks {
		t := parseTrakWithKind(sk, trak, size, handlerKinds[i])
		if t == nil {
			continue
		}
		seen[t.Kind]++
		t.ID = trackID(t.Kind, seen[t.Kind])
		// Re-label now that the identity is known; errors were created during
		// the parse, before the track had a name.
		for i, e := range t.Errors {
			t.Errors[i] = e.WithTrack(t.ID)
		}
		in.Tracks = append(in.Tracks, t)
	}
	sort.Slice(in.Tracks, func(i, j int) bool { return in.Tracks[i].ID < in.Tracks[j].ID })
	return in, nil
}

func trackID(kind Kind, n int) string {
	switch kind {
	case KindVideo:
		return "v" + itoa(int64(n-1))
	case KindAudio:
		return "a" + itoa(int64(n-1))
	default:
		return "t" + itoa(int64(n-1))
	}
}

// handlerKind reads a trak's media handler without parsing anything else.
func handlerKind(sk *seeker, trak *boxNode) Kind {
	hdlr := find(trak, "hdlr")
	if hdlr == nil {
		return ""
	}
	var hd mp4.Hdlr
	if e := unmarshalAt(sk, hdlr.Hdr, &hd); e != nil {
		return ""
	}
	switch string(hd.HandlerType[:]) {
	case "vide":
		return KindVideo
	case "soun":
		return KindAudio
	}
	return ""
}

func parseTrak(sk *seeker, trak *boxNode, fileSize int64) *Track {
	return parseTrakWithKind(sk, trak, fileSize, handlerKind(sk, trak))
}

func parseTrakWithKind(sk *seeker, trak *boxNode, fileSize int64, kind Kind) *Track {
	tkhd := find(trak, "tkhd")
	mdhd := find(trak, "mdhd")
	if tkhd == nil || mdhd == nil {
		return nil
	}
	if kind != KindVideo && kind != KindAudio {
		// Unsupported or unreadable handler: not a track this kernel processes.
		return nil
	}
	var mh mp4.Mdhd
	if e := unmarshalAt(sk, mdhd.Hdr, &mh); e != nil {
		t := &Track{Container: "mp4", Version: ParseVersion, SourceStart: trak.Hdr.Offset}
		t.AddError(e.WithTrack(""))
		return t
	}
	var tk mp4.Tkhd
	if e := unmarshalAt(sk, tkhd.Hdr, &tk); e != nil {
		t := &Track{Container: "mp4", Version: ParseVersion, SourceStart: trak.Hdr.Offset}
		t.AddError(e.WithTrack(""))
		return t
	}

	t := &Track{
		Container:   "mp4",
		Kind:        kind,
		Timescale:   mh.Timescale,
		Duration:    0,
		SourceStart: trak.Hdr.Offset,
		Version:     ParseVersion,
	}
	if lang, err := decodeISO639(mh.Language); err == nil {
		t.Language = lang
	}
	dur := uint64(mh.DurationV0)
	if mh.GetVersion() == 1 {
		dur = mh.DurationV1
	}
	if mh.Timescale == 0 {
		t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", mdhd.Hdr.Body,
			"mdhd timescale is zero"))
		return t
	}
	if d, err := media.SampleTicks(int64(dur), mh.Timescale); err == nil {
		t.Duration = d
	} else {
		t.AddError(media.NewError(media.ClassOutOfRange, media.StageParse, "", mdhd.Hdr.Offset,
			"track duration overflows the timeline"))
	}

	// Rotation matrix is always recorded raw, including its origin.
	t.RotMatrix = tk.Matrix
	t.RotSource = Source{Container: "mp4", Element: "moov/trak/tkhd/matrix", Offset: tkhd.Hdr.Offset}
	t.Rotation = rotationDegrees(tk.Matrix)

	entryW, entryH := visualEntrySize(sk, trak)
	t.FrameWidth = int64(tk.Width >> 16)
	t.FrameHeight = int64(tk.Height >> 16)
	if t.FrameWidth == 0 {
		t.FrameWidth = entryW
	}
	if t.FrameHeight == 0 {
		t.FrameHeight = entryH
	}

	parseEditList(sk, trak, mh.Timescale, t)
	if kind == KindVideo {
		parseClap(sk, trak, t)
		parseColors(sk, trak, t)
	}
	parseSamples(sk, trak, t, fileSize)

	// A truncated trak leaves part of the sample table unreadable. Mark the
	// track truncated at the end of whatever sample table could be read, then
	// attribute the structural error so the manifest names the track.
	if e := firstError(trak); e != nil {
		if e.Class == media.ClassTruncatedInput {
			t.Truncated = true
			t.TruncatedOffset = e.Offset
			if len(t.Samples) > 0 {
				t.TruncatedAt = t.Samples[len(t.Samples)-1].End()
			} else {
				t.TruncatedAt = 0
			}
		}
		t.AddError(e.WithTrack(t.ID))
	}
	// Every error recorded so far must carry the track identity, including
	// the ones created before the ID was assigned.
	for i, e := range t.Errors {
		t.Errors[i] = e.WithTrack(t.ID)
	}
	return t
}

// decodeISO639 expands the packed 5-bit language code. All-zero means
// unspecified, which is reported as no language rather than a fake one.
func decodeISO639(lang [3]byte) (string, error) {
	if lang == [3]byte{} {
		return "", errNoLanguage
	}
	var b [3]byte
	for i, c := range lang {
		b[i] = c + 0x60
	}
	return string(b[:]), nil
}

var errNoLanguage = errors.New("unspecified language")

// rotationDegrees decodes the clockwise display rotation from a tkhd matrix.
// Unusual matrices still rotate by the nearest supported quarter turn, while
// the raw matrix stays available for callers that need the difference.
func rotationDegrees(m [9]int32) int {
	// Values are 16.16 fixed point except the last column (2.30).
	a, b, c, d := int64(m[0]), int64(m[1]), int64(m[3]), int64(m[4])
	const one = 1 << 16
	near := func(v, target int64) bool { return v >= target-one/2 && v <= target+one/2 }
	switch {
	case near(a, one) && near(d, one) && near(b, 0) && near(c, 0):
		return 0
	case near(a, 0) && near(d, 0) && near(b, one) && near(c, -one):
		return 90
	case near(a, -one) && near(d, -one) && near(b, 0) && near(c, 0):
		return 180
	case near(a, 0) && near(d, 0) && near(b, -one) && near(c, one):
		return 270
	}
	return 0
}

func visualEntrySize(sk *seeker, trak *boxNode) (int64, int64) {
	stsd := find(trak, "stsd")
	if stsd == nil {
		return 0, 0
	}
	var w, h int64
	for _, entry := range stsd.Children {
		if !sampleEntryTypes[entry.Hdr.Type] || sampleEntryHeaderSize(entry.Hdr.Type) == 28 {
			continue
		}
		var vse mp4.VisualSampleEntry
		if e := unmarshalAt(sk, entry.Hdr, &vse); e == nil {
			w, h = int64(vse.Width), int64(vse.Height)
			break
		}
	}
	return w, h
}

func parseEditList(sk *seeker, trak *boxNode, timescale uint32, t *Track) {
	elst := find(trak, "elst")
	if elst == nil {
		return
	}
	var el mp4.Elst
	if e := unmarshalAt(sk, elst.Hdr, &el); e != nil {
		t.AddError(e)
		return
	}
	if len(el.Entries) == 0 {
		return
	}
	e := el.Entries[0]
	mediaTime := int64(e.MediaTimeV0)
	if el.GetVersion() == 1 {
		mediaTime = e.MediaTimeV1
	}
	if mediaTime <= 0 {
		return
	}
	off, err := media.SampleTicks(mediaTime, timescale)
	if err != nil {
		t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", elst.Hdr.Offset,
			"edit list media time cannot be converted"))
		return
	}
	t.EditOffset = -off
}

// parseClap reads a clean aperture box. The rectangle is validated against the
// encoded frame; an impossible crop rejects the track and keeps the clap
// offset as the first diagnostic position.
func parseClap(sk *seeker, trak *boxNode, t *Track) {
	clap := find(trak, "clap")
	if clap == nil {
		return
	}
	if clap.Hdr.PayloadSize() < 32 {
		t.AddError(media.NewError(media.ClassCorruptInput, media.StageParse, "", clap.Hdr.Body,
			"clean aperture box too short"))
		return
	}
	buf, e := readPayload(sk.r, clap.Hdr)
	if e != nil {
		t.AddError(e)
		return
	}
	u := make([]uint32, 8)
	for i := 0; i < 8; i++ {
		u[i] = binary.BigEndian.Uint32(buf[i*4:])
	}
	wN, wD, hN, hD := u[0], u[1], u[2], u[3]
	offXN, offXD := int32(u[4]), u[5]
	offYN, offYD := int32(u[6]), u[7]
	if wD == 0 || hD == 0 || offXD == 0 || offYD == 0 {
		t.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "", clap.Hdr.Offset,
			"clean aperture has a zero denominator"))
		return
	}
	width := int64(wN) / int64(wD)
	height := int64(hN) / int64(hD)
	// Offsets are 16.16 fixed point relative to the frame centre.
	offX := (int64(offXN) >> 16) / int64(offXD)
	offY := (int64(offYN) >> 16) / int64(offYD)
	left := (t.FrameWidth-width)/2 + offX
	top := (t.FrameHeight-height)/2 + offY
	a := &Aperture{
		Left: left, Top: top, Width: width, Height: height,
		Source: Source{Container: "mp4", Element: "moov/trak/mdia/minf/stbl/stsd/clap", Offset: clap.Hdr.Offset},
	}
	if err := a.Validate(t.FrameWidth, t.FrameHeight); err != nil {
		// Keep the raw rectangle for diagnosis but reject the track.
		t.Crop = a
		t.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "", clap.Hdr.Offset, err.Error()))
		return
	}
	t.Crop = a
}

// parseColors reads every colr box in the track. Inconsistent descriptions are
// recorded as conflicts with both origins; none is silently preferred.
func parseColors(sk *seeker, trak *boxNode, t *Track) {
	for _, colr := range findAll(trak, "colr") {
		if colr.Hdr.PayloadSize() < 4 {
			t.AddError(media.NewError(media.ClassCorruptInput, media.StageParse, "", colr.Hdr.Body,
				"colr box too short"))
			continue
		}
		buf, e := readPayload(sk.r, colr.Hdr)
		if e != nil {
			t.AddError(e)
			continue
		}
		c := ColorDescription{
			Source: Source{Container: "mp4", Element: "colr", Offset: colr.Hdr.Offset},
			Raw:    hexRaw(buf),
		}
		switch string(buf[0:4]) {
		case "nclx":
			if len(buf) < 11 {
				t.AddError(media.NewError(media.ClassCorruptInput, media.StageParse, "", colr.Hdr.Body,
					"nclx color box too short"))
				continue
			}
			c.Primaries = binary.BigEndian.Uint16(buf[4:])
			c.Transfer = binary.BigEndian.Uint16(buf[6:])
			c.Matrix = binary.BigEndian.Uint16(buf[8:])
			c.FullRange = buf[10]&0x80 != 0
			c.HasFullRange = true
			c.Interpretation = "nclx"
		case "prof", "rICC":
			c.Profile = append([]byte(nil), buf[4:]...)
			c.Interpretation = "icc"
			c.RequiresRendering = true
		default:
			c.Interpretation = "unknown"
			c.RequiresRendering = true
		}
		t.Colors = append(t.Colors, c)
	}
	t.Conflicts = colorConflicts(t.Colors)
}

// colorConflicts compares descriptions that state numeric color properties.
// ICC profiles are not numerically comparable and stay out of the comparison
// but remain in the track's color list.
func colorConflicts(colors []ColorDescription) []ColorConflict {
	var nclx []ColorDescription
	for _, c := range colors {
		if c.Interpretation == "nclx" || c.Interpretation == "mxf-property" {
			nclx = append(nclx, c)
		}
	}
	if len(nclx) < 2 {
		return nil
	}
	fields := []struct {
		name string
		get  func(ColorDescription) string
	}{
		{"primaries", func(c ColorDescription) string { return itoa(int64(c.Primaries)) }},
		{"transfer", func(c ColorDescription) string { return itoa(int64(c.Transfer)) }},
		{"matrix", func(c ColorDescription) string { return itoa(int64(c.Matrix)) }},
		{"full_range", func(c ColorDescription) string {
			if c.FullRange {
				return "true"
			}
			return "false"
		}},
	}
	var out []ColorConflict
	for _, f := range fields {
		first := f.get(nclx[0])
		same := true
		for _, c := range nclx[1:] {
			if f.get(c) != first {
				same = false
				break
			}
		}
		if same {
			continue
		}
		conf := ColorConflict{Field: f.name}
		seen := map[string]bool{}
		for _, c := range nclx {
			v := f.get(c)
			key := v + "|" + itoa(c.Source.Offset)
			if seen[key] {
				continue
			}
			seen[key] = true
			conf.Values = append(conf.Values, ColorValue{Field: f.name, Value: v, Source: c.Source})
		}
		out = append(out, conf)
	}
	return out
}

// parseSamples expands stts/stsc/stco/stsz into the sample table and places
// every sample on the shared nanosecond timeline. Offsets are computed in one
// linear pass so memory and time stay proportional to the sample table, not to
// how the samples are distributed across chunks.
func parseSamples(sk *seeker, trak *boxNode, t *Track, fileSize int64) {
	stbl := find(trak, "stbl")
	if stbl == nil {
		return
	}
	sttsNode := find(stbl, "stts")
	if sttsNode == nil {
		return
	}
	if !boxReadable(sttsNode, fileSize) {
		return
	}
	var stts mp4.Stts
	if e := unmarshalAt(sk, sttsNode.Hdr, &stts); e != nil {
		t.AddError(e)
		return
	}
	var stsz mp4.Stsz
	if n := find(stbl, "stsz"); n != nil && boxReadable(n, fileSize) {
		if e := unmarshalAt(sk, n.Hdr, &stsz); e != nil {
			t.AddError(e)
		}
	}
	var chunkOffsets []uint64
	if chunkNode := find(stbl, "stco"); chunkNode != nil && boxReadable(chunkNode, fileSize) {
		var stco mp4.Stco
		if e := unmarshalAt(sk, chunkNode.Hdr, &stco); e != nil {
			t.AddError(e)
		}
		for _, o := range stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	} else if chunkNode := find(stbl, "co64"); chunkNode != nil && boxReadable(chunkNode, fileSize) {
		var co64 mp4.Co64
		if e := unmarshalAt(sk, chunkNode.Hdr, &co64); e != nil {
			t.AddError(e)
		}
		chunkOffsets = append(chunkOffsets, co64.ChunkOffset...)
	}
	var stsc mp4.Stsc
	if n := find(stbl, "stsc"); n != nil && boxReadable(n, fileSize) {
		if e := unmarshalAt(sk, n.Hdr, &stsc); e != nil {
			t.AddError(e)
		}
	}
	keyframes := map[uint32]bool{}
	if n := find(stbl, "stss"); n != nil && boxReadable(n, fileSize) {
		var stss mp4.Stss
		if e := unmarshalAt(sk, n.Hdr, &stss); e == nil {
			for _, sn := range stss.SampleNumber {
				keyframes[sn] = true
			}
		}
	}

	offsets := sampleOffsets(&stsz, &stsc, chunkOffsets)

	var dts int64
	idx := 0
	sampleNum := uint32(0)
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			sampleNum++
			size := sampleSizeAt(&stsz, idx)
			var off int64 = -1
			if idx < len(offsets) {
				off = offsets[idx]
			}
			// Both edges are converted from cumulative decode times. Deriving
			// the duration as the difference keeps samples contiguous: summing
			// per-sample floored durations would drift below the declared
			// duration on long tracks.
			start, err := media.SampleTicks(dts, t.Timescale)
			if err != nil {
				t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", sttsNode.Hdr.Offset,
					"sample time conversion failed"))
				return
			}
			nextEnd, err := media.SampleTicks(dts+int64(e.SampleDelta), t.Timescale)
			if err != nil {
				t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", sttsNode.Hdr.Offset,
					"sample duration conversion failed"))
				return
			}
			dur := nextEnd - start
			start += t.EditOffset
			if start < 0 {
				t.AddError(media.NewError(media.ClassTimeRegression, media.StageParse, "", sttsNode.Hdr.Offset,
					"sample presentation time is negative"))
				return
			}
			if n := len(t.Samples); n > 0 && start < t.Samples[n-1].Start {
				t.AddError(media.NewError(media.ClassTimeRegression, media.StageParse, "", sttsNode.Hdr.Offset,
					"sample times go backwards"))
				return
			}
			t.Samples = append(t.Samples, Sample{
				Index: idx, Offset: off, Size: size, Start: start, Duration: dur,
				Keyframe: len(keyframes) == 0 || keyframes[sampleNum],
			})
			if off >= 0 && int64(size) > 0 && off+int64(size) > fileSize {
				// Sample data runs off the end of the file. Report the first
				// missing byte, drop this and every later sample (their
				// offsets cannot be trusted once the tail is gone) and keep
				// the readable prefix addressable.
				t.Truncated = true
				t.TruncatedAt = start
				t.TruncatedOffset = fileSize
				t.AddError(media.NewError(media.ClassTruncatedInput, media.StageParse, "", fileSize,
					"sample data extends past end of file"))
				return
			}
			dts += int64(e.SampleDelta)
			idx++
		}
	}
}

// sampleOffsets maps each sample index to its absolute file offset. Chunk runs
// are walked once; a sample whose chunk is undeclared gets -1.
func sampleOffsets(stsz *mp4.Stsz, stsc *mp4.Stsc, chunkOffsets []uint64) []int64 {
	total := int(stsz.SampleCount)
	if total == 0 {
		return nil
	}
	offsets := make([]int64, total)
	for i := range offsets {
		offsets[i] = -1
	}
	sample := 0
	for ci, base := range chunkOffsets {
		chunk := uint32(ci + 1)
		per := samplesPerChunk(stsc, chunk)
		if per == 0 {
			continue
		}
		off := int64(base)
		for k := uint32(0); k < per && sample < total; k++ {
			offsets[sample] = off
			off += int64(sampleSizeAt(stsz, sample))
			sample++
		}
		if sample >= total {
			break
		}
	}
	return offsets
}

// sampleSizeAt returns the byte size of sample i, honouring the fixed-size
// stsz form.
func sampleSizeAt(stsz *mp4.Stsz, i int) uint32 {
	if stsz.SampleSize != 0 {
		return stsz.SampleSize
	}
	if i >= 0 && i < len(stsz.EntrySize) {
		return stsz.EntrySize[i]
	}
	return 0
}

// samplesPerChunk resolves the stsc run covering chunk (1-based).
func samplesPerChunk(stsc *mp4.Stsc, chunk uint32) uint32 {
	var per uint32
	for _, e := range stsc.Entries {
		if e.FirstChunk <= chunk {
			per = e.SamplesPerChunk
		} else {
			break
		}
	}
	return per
}

func readPayload(r io.ReaderAt, h boxHeader) ([]byte, *media.Error) {
	n := h.PayloadSize()
	if n < 0 || n > 64<<20 {
		return nil, media.NewError(media.ClassCorruptInput, media.StageParse, "", h.Offset,
			"box payload implausible size "+itoa(n))
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	got, err := r.ReadAt(buf, h.Body)
	if int64(got) < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, media.WrapError(media.ClassTruncatedInput, media.StageParse, "", h.Body+int64(got),
			"truncated box payload", err)
	}
	return buf, nil
}

func unmarshalAt(sk *seeker, h boxHeader, dst mp4.IBox) *media.Error {
	sk.pos = h.Body
	if _, err := mp4.Unmarshal(sk, uint64(h.PayloadSize()), dst, mp4.Context{}); err != nil {
		return media.WrapError(media.ClassCorruptInput, media.StageParse, "", h.Body,
			"cannot parse box "+h.Type, err)
	}
	return nil
}

func hexRaw(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		const hexDigits = "0123456789abcdef"
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0xf])
	}
	return sb.String()
}
