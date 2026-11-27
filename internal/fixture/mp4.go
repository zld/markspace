package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"regress/internal/clock"
	"regress/internal/model"
)

// SmpcBoxType is the custom "sample catalog" box carried inside each trak.
// It is parsed manually by the ingest reader rather than by go-mp4's
// registry, so the on-disk layout is fully controlled by this package.
var SmpcBoxType = mp4.StrToBoxType("smpc")

// Entry type tags inside an smpc catalog.
const (
	EntryVideo byte = 1
	EntryAudio byte = 2
	EntryEvent byte = 3
)

// memFile is an in-memory io.WriteSeeker / io.ReadSeeker whose write cursor
// is independent of its length: seeking backwards and writing overwrites the
// targeted bytes (go-mp4 rewrites box headers this way) without truncating
// the bytes that follow.
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memFile) Read(p []byte) (int, error) {
	if m.pos >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[m.pos:])
	m.pos += int64(n)
	return n, nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = m.pos + offset
	case io.SeekEnd:
		abs = int64(len(m.buf)) + offset
	}
	if abs < 0 {
		return 0, fmt.Errorf("seek out of range: %d", abs)
	}
	m.pos = abs
	return abs, nil
}

func (m *memFile) Bytes() []byte { return m.buf }

// lcg is the deterministic pixel/payload generator shared by writer and any
// code that needs to reason about fixture content.
type lcg struct{ state uint32 }

func (l *lcg) next() uint32 {
	l.state = l.state*1103515245 + 12345
	return l.state
}

// FramePixels renders frame seq as FrameW*FrameH grayscale bytes.
func FramePixels(seed uint32, seq uint32) []byte {
	r := lcg{state: seed ^ (seq*2654435761 + 1)}
	buf := make([]byte, FrameW*FrameH)
	for i := range buf {
		buf[i] = byte(r.next() >> 24)
	}
	return buf
}

// AudioSamples renders one audio sample as AudioFrames int16 PCM values.
func AudioSamples(seed uint32, seq uint32) []int16 {
	r := lcg{state: seed ^ (seq*40503 + 7)}
	buf := make([]int16, AudioFrames)
	for i := range buf {
		buf[i] = int16(r.next()>>16) - 16384
	}
	return buf
}

type trackSpec struct {
	id        string
	kind      model.TrackKind
	timescale int64
}

func allTracks() []trackSpec {
	return []trackSpec{
		{RendHigh, model.KindVideo, VideoHz},
		{RendLow, model.KindVideo, VideoHz},
		{AudioID, model.KindAudio, AudioHz},
		{MetaID3, model.KindID3, VideoHz},
		{MetaEmsg, model.KindEmsg, VideoHz},
		{MetaSEI, model.KindSEI, VideoHz},
		{MetaSub, model.KindSubtitle, VideoHz},
		{MetaChap, model.KindChapter, VideoHz},
	}
}

func startBox(w *mp4.Writer, btype mp4.BoxType) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: btype})
	return err
}

func endBox(w *mp4.Writer) error {
	_, err := w.EndBox()
	return err
}

func hdlrType(kind model.TrackKind) [4]byte {
	var out [4]byte
	s := string(kind)
	for i := 0; i < 4 && i < len(s); i++ {
		out[i] = s[i]
	}
	return out
}

func marshalBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, mp4.Context{})
	return err
}

// BuildMP4 serializes the complete fixture container. For FaultTruncate it
// also returns the byte offset where the container must be cut; callers cut
// the buffer themselves so the truncated file on disk is visibly shorter.
func BuildMP4(s Spec, plan []model.PlanItem) (data []byte, cutOffset int64, err error) {
	f := &memFile{}
	w := mp4.NewWriter(f)

	ftyp := &mp4.Ftyp{MajorBrand: [4]byte{'r', 'g', 'r', 's'}, MinorVersion: 1}
	if err = startBox(w, mp4.BoxTypeFtyp()); err != nil {
		return nil, 0, err
	}
	if err = marshalBox(w, ftyp); err != nil {
		return nil, 0, err
	}
	if err = endBox(w); err != nil {
		return nil, 0, err
	}

	if err = startBox(w, mp4.BoxTypeMoov()); err != nil {
		return nil, 0, err
	}
	for _, tr := range allTracks() {
		if err = startBox(w, mp4.BoxTypeTrak()); err != nil {
			return nil, 0, err
		}
		if err = writeTrak(w, s, plan, tr); err != nil {
			return nil, 0, err
		}
		if err = endBox(w); err != nil {
			return nil, 0, err
		}
		// The truncation fault cuts inside the first smpc catalog, and its
		// recorded offset points at the box header for error reporting.
		if s.Fault == FaultTruncate && tr.id == RendHigh {
			pos, _ := f.Seek(0, io.SeekCurrent)
			cutOffset = pos - 12
		}
	}
	if err = writeMoovEmsg(w, plan); err != nil {
		return nil, 0, err
	}
	if err = endBox(w); err != nil {
		return nil, 0, err
	}

	if err = writeMdat(w, s, plan); err != nil {
		return nil, 0, err
	}
	return f.Bytes(), cutOffset, nil
}

func writeTrak(w *mp4.Writer, s Spec, plan []model.PlanItem, tr trackSpec) error {
	if err := startBox(w, mp4.BoxTypeMdia()); err != nil {
		return err
	}
	duration := int64(s.VideoFrames) * int64(VideoFrameTick)
	if tr.kind == model.KindAudio {
		duration = int64(s.VideoFrames) * AudioHz / 30
	}
	mdhd := &mp4.Mdhd{Timescale: uint32(tr.timescale), DurationV0: uint32(duration)}
	if err := startBox(w, mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	if err := marshalBox(w, mdhd); err != nil {
		return err
	}
	if err := endBox(w); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: hdlrType(tr.kind), Name: tr.id}
	if err := startBox(w, mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	if err := marshalBox(w, hdlr); err != nil {
		return err
	}
	if err := endBox(w); err != nil {
		return err
	}
	if err := startBox(w, SmpcBoxType); err != nil {
		return err
	}
	cat, err := buildCatalog(s, plan, tr)
	if err != nil {
		return err
	}
	if _, err := w.Write(cat); err != nil {
		return err
	}
	return endBox(w)
}

// trSeed returns the deterministic pixel/PCM seed writeMdat used for this
// track's samples, so the reader can reproduce the exact stored bytes.
func trSeed(s Spec, tr trackSpec) uint32 {
	switch tr.id {
	case RendLow:
		return s.Seed ^ 0xffff
	default:
		return s.Seed
	}
}

// buildCatalog renders the binary smpc payload for one track.
func buildCatalog(s Spec, plan []model.PlanItem, tr trackSpec) ([]byte, error) {
	var b bytes.Buffer
	writeU32 := func(v uint32) {
		var tmp [4]byte
		binary.BigEndian.PutUint32(tmp[:], v)
		b.Write(tmp[:])
	}
	writeU64 := func(v uint64) {
		var tmp [8]byte
		binary.BigEndian.PutUint64(tmp[:], v)
		b.Write(tmp[:])
	}
	writeU32(uint32(tr.timescale))
	// The generation seed lets the reader reproduce the exact media bytes
	// that writeMdat stored, so evidence hashes come from real sample
	// content rather than an assumed default.
	writeU32(trSeed(s, tr))
	switch tr.kind {
	case model.KindVideo:
		writeU32(uint32(s.VideoFrames))
		for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
			tick := int64(seq) * int64(VideoFrameTick)
			src := tick * tr.timescale / clock.MasterHz
			b.WriteByte(EntryVideo)
			writeU32(seq)
			writeU64(uint64(src))
			writeU32(FrameW)
			writeU32(FrameH)
		}
	case model.KindAudio:
		writeU32(uint32(s.VideoFrames))
		for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
			src := int64(seq) * AudioHz / 30
			b.WriteByte(EntryAudio)
			writeU32(seq)
			writeU64(uint64(src))
			writeU32(AudioFrames)
		}
	default:
		var entries []model.PlanItem
		for _, it := range plan {
			if it.Track == tr.id && !it.Drop {
				entries = append(entries, it)
				if it.Duplicate {
					entries = append(entries, it) // same key, repeated
				}
			}
		}
		writeU32(uint32(len(entries)))
		for _, it := range entries {
			// Catalogs carry the observed presentation time, including the
			// planned publish delay, so the reader reconstructs real delay.
			observed := int64(it.ExpectedTick) + int64(it.DelayTicks)
			src := observed * tr.timescale / clock.MasterHz
			b.WriteByte(EntryEvent)
			writeU32(it.Seq)
			writeU64(uint64(src))
			keyb := []byte(it.Key)
			writeU32(uint32(len(keyb)))
			b.Write(keyb)
		}
	}
	return b.Bytes(), nil
}
