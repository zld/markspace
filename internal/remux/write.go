// Package remux rewrites container structure only. It copies every sample
// payload byte-for-byte from the source, rewrites fragment boxes and their
// timestamps, and never decodes or re-encodes media.
package remux

import (
	"bytes"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// Sample is one sample to carry into the output, with its source byte range.
type Sample struct {
	TrackID           uint32
	SourceOffset      int64 // raw offset in the source file
	Size              uint32
	Duration          uint32
	CompositionOffset int32
	Keyframe          bool
	// Time is the sample's decode time in the track timescale after remuxing.
	Time uint64
	// Payload, when non-nil, is written instead of reading from the source.
	// Transport streams scatter an access unit across packets, so their bytes
	// are supplied directly; SourceOffset is then only diagnostic.
	Payload []byte
}

// Track describes one output track and the samples to write.
type Track struct {
	InputID    uint32 // track id in the source container
	OutputID   uint32 // track id in the remuxed output
	Timescale  uint32
	Handler    string
	Codec      string
	Channels   int
	SampleSize int
	SampleRate int
	Width      int
	Height     int
	Language   [3]byte
	// InitBytes is the source init segment; its sample entries are copied so
	// codec configuration travels untouched.
	InitBytes []byte
	Samples   []Sample
}

// FragmentPlan is one output fragment's sample range per track.
type FragmentPlan struct {
	Sequence       uint32
	TrackID        uint32
	BaseDecodeTime uint64
	Samples        []Sample
}

// Options bounds resource use.
type Options struct {
	// MaxFragmentSamples caps samples per output fragment.
	MaxFragmentSamples int
	// MaxTotalBytes caps the output size.
	MaxTotalBytes int64
}

func (o Options) withDefaults() Options {
	if o.MaxFragmentSamples <= 0 {
		o.MaxFragmentSamples = 4096
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = 512 << 20
	}
	return o
}

// Result reports what was written.
type Result struct {
	Path      string
	Bytes     int64
	Fragments int
	Samples   int
	// InFragments maps output sample identity to the source fragment sequence
	// so verification can align pre/post remux content.
}

// Writer writes an init segment and fragmented media segments.
type Writer struct {
	opts    Options
	src     io.ReadSeeker
	path    string
	out     *os.File
	buf     []byte
	written int64
}

// NewWriter opens output for writing. src is the seekable source used to copy
// sample bytes; path and srcPath are used in diagnostics.
func NewWriter(outPath, srcPath string, src io.ReadSeeker, opts Options) (*Writer, error) {
	f, err := os.Create(outPath)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, outPath, -1, "create remux output", err)
	}
	return &Writer{opts: opts.withDefaults(), src: src, path: srcPath, out: f,
		buf: make([]byte, 64*1024)}, nil
}

// Close flushes and closes the output.
func (w *Writer) Close() error { return w.out.Close() }

// Bytes returns the number of bytes written.
func (w *Writer) Bytes() int64 { return w.written }

func (w *Writer) writeCounted(p []byte) error {
	if w.written+int64(len(p)) > w.opts.MaxTotalBytes {
		return fault.New(fault.KindLimit, w.path, w.written,
			"remux output exceeds the configured byte limit").Recover(false)
	}
	n, err := w.out.Write(p)
	w.written += int64(n)
	if err != nil {
		return fault.Wrap(fault.KindPath, w.path, w.written, "write remux output", err)
	}
	return nil
}

// WriteInit writes ftyp + moov. Track sample entries are rebuilt from the
// codec parameters; no media bytes are touched.
func (w *Writer) WriteInit(tracks []Track) error {
	var buf bytes.Buffer
	ws := &seekBuffer{buf: &buf}
	mw := mp4.NewWriter(ws)

	ftyp := &mp4.Ftyp{MajorBrand: mp4.BrandISOM(), MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISO6()},
			{CompatibleBrand: [4]byte{'i', 'p', 'c', 'm'}},
			{CompatibleBrand: [4]byte{'d', 'a', 's', 'h'}},
		}}
	if err := writeBox(mw, mp4.BoxTypeFtyp(), ftyp, mp4.Context{}); err != nil {
		return w.wrap(err, -1)
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return w.wrap(err, -1)
	}
	var maxDur uint64
	for _, t := range tracks {
		var dur uint64
		for _, s := range t.Samples {
			dur += uint64(s.Duration)
		}
		if dur > maxDur {
			maxDur = dur
		}
	}
	mvhd := &mp4.Mvhd{Timescale: 1000, DurationV0: uint32(maxDur), Rate: 0x00010000,
		Volume: 0x0100, Matrix: identityMatrix(), NextTrackID: uint32(len(tracks) + 1)}
	if err := writeBox(mw, mp4.BoxTypeMvhd(), mvhd, mp4.Context{}); err != nil {
		return w.wrap(err, -1)
	}
	for _, t := range tracks {
		if err := w.writeTrak(mw, t); err != nil {
			return err
		}
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvex()}); err != nil {
		return w.wrap(err, -1)
	}
	for _, t := range tracks {
		trex := &mp4.Trex{TrackID: t.OutputID, DefaultSampleDescriptionIndex: 1}
		if err := writeBox(mw, mp4.BoxTypeTrex(), trex, mp4.Context{}); err != nil {
			return w.wrap(err, -1)
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return w.wrap(err, -1)
	}
	if _, err := mw.EndBox(); err != nil {
		return w.wrap(err, -1)
	}
	return w.writeCounted(buf.Bytes())
}

func (w *Writer) writeTrak(mw *mp4.Writer, t Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return w.wrap(err, -1)
	}
	var dur uint64
	for _, s := range t.Samples {
		dur += uint64(s.Duration)
	}
	tkhd := &mp4.Tkhd{TrackID: t.OutputID, DurationV0: uint32(clock.Rational{Num: int64(dur), Den: int64(t.Timescale)}.Ticks(1000)),
		Volume: 0x0100, Matrix: identityMatrix()}
	tkhd.SetFlags(0x000007)
	if err := writeBox(mw, mp4.BoxTypeTkhd(), tkhd, mp4.Context{}); err != nil {
		return w.wrap(err, -1)
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return w.wrap(err, -1)
	}
	lang := t.Language
	if lang == [3]byte{} {
		lang = [3]byte{'u', 'n', 'd'}
	}
	mdhd := &mp4.Mdhd{Timescale: t.Timescale, DurationV0: uint32(dur), Language: lang}
	if err := writeBox(mw, mp4.BoxTypeMdhd(), mdhd, mp4.Context{}); err != nil {
		return w.wrap(err, -1)
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{t.Handler[0], t.Handler[1], t.Handler[2], t.Handler[3]}, Name: "liveregress"}
	if err := writeBox(mw, mp4.BoxTypeHdlr(), hdlr, mp4.Context{}); err != nil {
		return w.wrap(err, -1)
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return w.wrap(err, -1)
	}
	if t.Handler == "vide" {
		vmhd := &mp4.Vmhd{}
		if err := writeBox(mw, mp4.BoxTypeVmhd(), vmhd, mp4.Context{}); err != nil {
			return w.wrap(err, -1)
		}
	} else {
		smhd := &mp4.Smhd{}
		if err := writeBox(mw, mp4.BoxTypeSmhd(), smhd, mp4.Context{}); err != nil {
			return w.wrap(err, -1)
		}
	}
	if err := writeDinf(mw); err != nil {
		return w.wrap(err, -1)
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return w.wrap(err, -1)
	}
	if err := writeStsd(mw, t); err != nil {
		return w.wrap(err, -1)
	}
	for _, st := range []mp4.IImmutableBox{emptyStts(), emptyStsc(), emptyStsz(), emptyStco()} {
		if err := writeBox(mw, st.GetType(), st, mp4.Context{}); err != nil {
			return w.wrap(err, -1)
		}
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return w.wrap(err, -1)
	}
	if _, err := mw.EndBox(); err != nil { // minf
		return w.wrap(err, -1)
	}
	if _, err := mw.EndBox(); err != nil { // mdia
		return w.wrap(err, -1)
	}
	_, err := mw.EndBox() // trak
	return w.wrap(err, -1)
}

func (w *Writer) wrap(err error, off int64) error {
	if err == nil {
		return nil
	}
	return fault.Wrap(fault.KindFormat, w.path, off, "build remux init", err)
}

// WriteFragment writes one moof+mdat for a single track, copying the sample
// bytes verbatim from the source at their recorded offsets.
func (w *Writer) WriteFragment(plan FragmentPlan) (int64, error) {
	if len(plan.Samples) == 0 {
		return 0, fault.New(fault.KindFormat, w.path, -1, "fragment has no samples")
	}
	var data bytes.Buffer
	for _, s := range plan.Samples {
		if int64(s.Size) > 64<<20 {
			return 0, fault.New(fault.KindLimit, w.path, s.SourceOffset,
				fmt.Sprintf("sample size %d exceeds the per-sample limit", s.Size)).Recover(false)
		}
		if s.Payload != nil {
			if uint32(len(s.Payload)) != s.Size {
				return 0, fault.New(fault.KindCorrupt, w.path, s.SourceOffset,
					fmt.Sprintf("payload length %d does not match declared size %d", len(s.Payload), s.Size)).Recover(false)
			}
			data.Write(s.Payload)
			continue
		}
		if _, err := w.src.Seek(s.SourceOffset, io.SeekStart); err != nil {
			return 0, fault.Wrap(fault.KindCorrupt, w.path, s.SourceOffset, "seek source sample", err)
		}
		chunk := make([]byte, s.Size)
		if _, err := io.ReadFull(w.src, chunk); err != nil {
			return 0, fault.Wrap(fault.KindTruncate, w.path, s.SourceOffset,
				"truncated source sample", err).Recover(false)
		}
		data.Write(chunk)
	}
	moof := &FragmentPlan{Sequence: plan.Sequence, TrackID: plan.TrackID,
		BaseDecodeTime: plan.BaseDecodeTime, Samples: plan.Samples}
	moofBytes, err := marshalMoof(moof, 0)
	if err != nil {
		return 0, w.wrap(err, -1)
	}
	dataOffset := int32(len(moofBytes) + 8)
	moofBytes, err = marshalMoof(moof, dataOffset)
	if err != nil {
		return 0, w.wrap(err, -1)
	}
	if err := w.writeCounted(moofBytes); err != nil {
		return 0, err
	}
	var hdr [8]byte
	mdatLen := uint32(data.Len() + 8)
	hdr[0], hdr[1], hdr[2], hdr[3] = byte(mdatLen>>24), byte(mdatLen>>16), byte(mdatLen>>8), byte(mdatLen)
	copy(hdr[4:], "mdat")
	if err := w.writeCounted(hdr[:]); err != nil {
		return 0, err
	}
	if err := w.writeCounted(data.Bytes()); err != nil {
		return 0, err
	}
	return int64(len(moofBytes) + 8 + data.Len()), nil
}

func marshalMoof(plan *FragmentPlan, dataOffset int32) ([]byte, error) {
	var buf bytes.Buffer
	mw := mp4.NewWriter(&seekBuffer{buf: &buf})
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return nil, err
	}
	mfhd := &mp4.Mfhd{SequenceNumber: plan.Sequence}
	if err := writeBox(mw, mp4.BoxTypeMfhd(), mfhd, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return nil, err
	}
	tfhd := &mp4.Tfhd{TrackID: plan.TrackID}
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof)
	if err := writeBox(mw, mp4.BoxTypeTfhd(), tfhd, mp4.Context{}); err != nil {
		return nil, err
	}
	tfdt := &mp4.Tfdt{}
	tfdt.Version = 1
	tfdt.BaseMediaDecodeTimeV1 = plan.BaseDecodeTime
	if err := writeBox(mw, mp4.BoxTypeTfdt(), tfdt, mp4.Context{}); err != nil {
		return nil, err
	}
	trun := &mp4.Trun{SampleCount: uint32(len(plan.Samples)), DataOffset: dataOffset}
	trun.SetFlags(0x000001 | 0x000100 | 0x000200 | 0x000400 | 0x000800)
	for _, s := range plan.Samples {
		var flags uint32
		if !s.Keyframe {
			flags = 0x00010000
		}
		trun.Entries = append(trun.Entries, mp4.TrunEntry{
			SampleDuration: s.Duration, SampleSize: s.Size, SampleFlags: flags,
			SampleCompositionTimeOffsetV0: uint32(s.CompositionOffset),
		})
	}
	if err := writeBox(mw, mp4.BoxTypeTrun(), trun, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := mw.EndBox(); err != nil { // traf
		return nil, err
	}
	if _, err := mw.EndBox(); err != nil { // moof
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeBox(w *mp4.Writer, typ mp4.BoxType, box mp4.IImmutableBox, ctx mp4.Context) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, ctx); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	dref := &mp4.Dref{EntryCount: 1}
	if _, err := mp4.Marshal(w, dref, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.FullBox.Flags = [3]byte{0, 0, 1}
	if err := writeBox(w, mp4.BoxTypeUrl(), url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, t Track) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	if t.Handler == "vide" {
		entry := &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.StrToBoxType(t.Codec)},
				DataReferenceIndex: 1},
			Width: uint16(t.Width), Height: uint16(t.Height), FrameCount: 1,
			Horizresolution: 0x00480000, Vertresolution: 0x00480000, Depth: 0x0018,
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(t.Codec)}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	} else {
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.StrToBoxType(t.Codec)},
				DataReferenceIndex: 1},
			ChannelCount: uint16(t.Channels), SampleSize: uint16(t.SampleSize),
			SampleRate: uint32(t.SampleRate) << 16,
		}
		if t.Codec == "ipcm" {
			if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeIpcm()}); err != nil {
				return err
			}
			if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
				return err
			}
			pcmC := &mp4.PcmC{PCMSampleSize: uint8(t.SampleSize)}
			if err := writeBox(w, mp4.BoxTypePcmC(), pcmC, mp4.Context{}); err != nil {
				return err
			}
			if _, err := w.EndBox(); err != nil {
				return err
			}
		} else {
			if err := writeBox(w, mp4.StrToBoxType(t.Codec), entry, mp4.Context{}); err != nil {
				return err
			}
		}
	}
	_, err := w.EndBox()
	return err
}

func emptyStts() *mp4.Stts { return &mp4.Stts{} }
func emptyStsc() *mp4.Stsc { return &mp4.Stsc{} }
func emptyStsz() *mp4.Stsz { return &mp4.Stsz{} }
func emptyStco() *mp4.Stco { return &mp4.Stco{} }

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// seekBuffer adapts bytes.Buffer to io.WriteSeeker for mp4.Writer.
type seekBuffer struct {
	buf *bytes.Buffer
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	if s.pos > int64(s.buf.Len()) {
		s.buf.Write(make([]byte, s.pos-int64(s.buf.Len())))
	}
	if s.pos == int64(s.buf.Len()) {
		s.pos += int64(len(p))
		return s.buf.Write(p)
	}
	data := s.buf.Bytes()
	end := s.pos + int64(len(p))
	if end > int64(len(data)) {
		grown := make([]byte, end)
		copy(grown, data)
		data = grown
	}
	copy(data[s.pos:], p)
	s.buf.Reset()
	s.buf.Write(data)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = int64(s.buf.Len()) + off
	}
	if s.pos < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	return s.pos, nil
}
