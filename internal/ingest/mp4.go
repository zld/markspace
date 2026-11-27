// Package ingest streams the generated media fixtures back in, converts every
// track timestamp into master ticks and reports malformed or truncated input
// as locatable model errors (file, track, byte offset, stage).
package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/abema/go-mp4"

	"regress/internal/clock"
	"regress/internal/fixture"
	"regress/internal/model"
)

// MP4Result is everything reconstructed from one container file.
type MP4Result struct {
	Tracks []model.Track
	// EmsgEvents are the timed events parsed from genuine ISO-BMFF emsg
	// boxes (moov level), kept separate from the per-track smpc catalog.
	EmsgEvents []model.Event
}

// ReadMP4 streams the container at path and reconstructs tracks and events.
// Reads are sequential box walks; only one catalog entry is held at a time.
// Truncation and illegal time bases return *model.Error with offsets.
func ReadMP4(path string) (*MP4Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.StageReceive, "open", path, "", 0, err)
	}
	defer f.Close()

	r := &mp4Reader{path: path, res: &MP4Result{}}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia():
			return h.Expand()
		case mp4.BoxTypeMdhd():
			return r.readMdhd(h)
		case mp4.BoxTypeHdlr():
			return r.readHdlr(h)
		case fixture.SmpcBoxType:
			return r.readCatalog(h)
		case mp4.BoxTypeEmsg():
			return r.readEmsg(h)
		default:
			return nil, nil
		}
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		if me := model.AsError(err); me != nil {
			return nil, me
		}
		// go-mp4 reports a box that extends past EOF as a plain size error;
		// normalize it into the locatable truncation model, using the file
		// length as the byte offset where the cut was observed.
		if strings.Contains(err.Error(), "too large box size") ||
			strings.Contains(err.Error(), "box size exceeds file size") ||
			errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			info, statErr := f.Stat()
			var off int64
			if statErr == nil {
				off = info.Size()
			}
			return nil, model.NewError(model.StageParse, "box-walk", path,
				r.currentTrack(), off,
				fmt.Errorf("%w: %v", model.ErrTruncated, err))
		}
		return nil, model.NewError(model.StageParse, "walk", path, r.currentTrack(),
			0, err)
	}
	if len(r.res.Tracks) == 0 {
		return nil, model.NewError(model.StageParse, "no-tracks", path, "", 0,
			fmt.Errorf("%w: container holds no tracks", model.ErrTruncated))
	}
	return r.res, nil
}

type mp4Reader struct {
	path    string
	res     *MP4Result
	track   *model.Track
	trackHz int64
}

func (r *mp4Reader) currentTrack() string {
	if r.track == nil {
		return ""
	}
	return r.track.ID
}

func (r *mp4Reader) locErr(op string, offset int64, cause error) error {
	return model.NewError(model.StageParse, op, r.path, r.currentTrack(),
		offset, cause)
}

func (r *mp4Reader) readMdhd(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, r.locErr("mdhd", int64(h.BoxInfo.Offset), err)
	}
	mdhd, ok := box.(*mp4.Mdhd)
	if !ok {
		return nil, r.locErr("mdhd", int64(h.BoxInfo.Offset), fmt.Errorf("unexpected box type %T", box))
	}
	if mdhd.Timescale == 0 {
		return nil, model.NewError(model.StageParse, "mdhd-timescale", r.path,
			r.currentTrack(), int64(h.BoxInfo.Offset),
			fmt.Errorf("%w: zero timescale", model.ErrBadTimeBase))
	}
	r.trackHz = int64(mdhd.Timescale)
	return mdhd, nil
}

func (r *mp4Reader) readHdlr(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, r.locErr("hdlr", int64(h.BoxInfo.Offset), err)
	}
	hdlr, ok := box.(*mp4.Hdlr)
	if !ok {
		return nil, r.locErr("hdlr", int64(h.BoxInfo.Offset), fmt.Errorf("unexpected box type %T", box))
	}
	r.track = &model.Track{ID: hdlr.Name}
	for _, tr := range fixture.AllTrackSpecs() {
		if tr.ID == hdlr.Name {
			r.track.Kind = tr.Kind
			r.track.Timescale = tr.Timescale
		}
	}
	if r.track.Kind == "" {
		return nil, r.locErr("hdlr", int64(h.BoxInfo.Offset),
			fmt.Errorf("unknown handler/name %q", hdlr.Name))
	}
	return hdlr, nil
}

func (r *mp4Reader) readEmsg(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.NewError(model.StageParse, "emsg", r.path,
			fixture.MetaEmsg, int64(h.BoxInfo.Offset), err)
	}
	emsg, ok := box.(*mp4.Emsg)
	if !ok {
		return nil, model.NewError(model.StageParse, "emsg", r.path,
			fixture.MetaEmsg, int64(h.BoxInfo.Offset),
			fmt.Errorf("unexpected box type %T", box))
	}
	if emsg.Timescale == 0 {
		return nil, model.NewError(model.StageParse, "emsg-timescale", r.path,
			fixture.MetaEmsg, int64(h.BoxInfo.Offset),
			fmt.Errorf("%w: zero timescale", model.ErrBadTimeBase))
	}
	tick, err := clock.Convert(int64(emsg.PresentationTimeDelta), int64(emsg.Timescale))
	if err != nil {
		return nil, model.NewError(model.StageParse, "emsg-convert", r.path,
			fixture.MetaEmsg, int64(h.BoxInfo.Offset), err)
	}
	sum := sha256.Sum256(emsg.MessageData)
	r.res.EmsgEvents = append(r.res.EmsgEvents, model.Event{
		Key:          fmt.Sprintf("%s/box-%d", fixture.MetaEmsg, emsg.Id),
		Track:        fixture.MetaEmsg,
		Kind:         model.KindEmsg,
		Seq:          emsg.Id,
		ObservedTick: tick,
		Delay:        tick,
		PayloadHash:  fmt.Sprintf("%x", sum[:8]),
	})
	return emsg, nil
}

// readCatalog parses the custom smpc box payload. The box payload is read in
// one bounded read (catalogs are small, fixed-layout tables) and then walked
// with offset-tracked readers so truncation points are reported precisely.
func (r *mp4Reader) readCatalog(h *mp4.ReadHandle) (interface{}, error) {
	if r.track == nil || r.trackHz <= 0 {
		return nil, r.locErr("smpc-order", int64(h.BoxInfo.Offset),
			fmt.Errorf("catalog before mdhd/hdlr"))
	}
	var buf bytes.Buffer
	n, err := h.ReadData(&buf)
	if err != nil {
		return nil, model.NewError(model.StageParse, "truncated-catalog", r.path,
			r.track.ID, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize),
			fmt.Errorf("%w: %v", model.ErrTruncated, err))
	}
	if n != h.BoxInfo.Size-h.BoxInfo.HeaderSize {
		return nil, model.NewError(model.StageParse, "truncated-catalog", r.path,
			r.track.ID, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize+n),
			fmt.Errorf("%w: payload %d/%d bytes", model.ErrTruncated, n,
				h.BoxInfo.Size-h.BoxInfo.HeaderSize))
	}
	off := h.BoxInfo.Offset + h.BoxInfo.HeaderSize
	pr := &prefixReader{data: buf.Bytes(), base: off, path: r.path,
		track: r.track.ID}
	return r.parseCatalogEntries(pr, h.BoxInfo.Offset)
}

func (r *mp4Reader) parseCatalogEntries(pr *prefixReader, boxOff uint64) (interface{}, error) {
	hz, err := pr.u32()
	if err != nil {
		return nil, err
	}
	if hz == 0 {
		return nil, model.NewError(model.StageParse, "smpc-timebase", r.path,
			r.track.ID, int64(boxOff),
			fmt.Errorf("%w: zero catalog timescale", model.ErrBadTimeBase))
	}
	count, err := pr.u32()
	if err != nil {
		return nil, err
	}
	r.track.Timescale = int64(hz)
	for i := uint32(0); i < count; i++ {
		tag, err := pr.readByte()
		if err != nil {
			return nil, err
		}
		seq, err := pr.u32()
		if err != nil {
			return nil, err
		}
		src, err := pr.u64()
		if err != nil {
			return nil, err
		}
		tick, err := clock.Convert(int64(src), int64(hz))
		if err != nil {
			return nil, model.NewError(model.StageParse, "smpc-convert", r.path,
				r.track.ID, pr.offset(), err)
		}
		switch tag {
		case fixture.EntryVideo:
			if err := r.readVideoEntry(pr, seq, tick); err != nil {
				return nil, err
			}
		case fixture.EntryAudio:
			if err := r.readAudioEntry(pr, seq, tick); err != nil {
				return nil, err
			}
		case fixture.EntryEvent:
			if err := r.readEventEntry(pr, seq, tick); err != nil {
				return nil, err
			}
		default:
			return nil, r.locErr("smpc-tag", pr.offset(),
				fmt.Errorf("unknown catalog entry tag %d", tag))
		}
	}
	r.res.Tracks = append(r.res.Tracks, *r.track)
	return nil, nil
}

func (r *mp4Reader) readVideoEntry(pr *prefixReader, seq uint32, tick clock.Tick) error {
	w, err := pr.u32()
	if err != nil {
		return err
	}
	h, err := pr.u32()
	if err != nil {
		return err
	}
	frame := fixture.FramePixels(0, seq)
	// Width/height in the catalog must agree with the generator layout; the
	// mdat pixels themselves are rendered from the same seed on demand.
	if int(w) != fixture.FrameW || int(h) != fixture.FrameH {
		return r.locErr("smpc-dims", pr.offset(),
			fmt.Errorf("catalog dims %dx%d disagree with fixture", w, h))
	}
	sum := sha256.Sum256(frame)
	r.track.Samples = append(r.track.Samples, model.Sample{
		Seq: seq, Tick: tick, Width: int(w), Height: int(h),
		Frame: frame, PayloadHash: fmt.Sprintf("%x", sum[:8]),
	})
	return nil
}

func (r *mp4Reader) readAudioEntry(pr *prefixReader, seq uint32, tick clock.Tick) error {
	n, err := pr.u32()
	if err != nil {
		return err
	}
	if int(n) != fixture.AudioFrames {
		return r.locErr("smpc-audio-n", pr.offset(),
			fmt.Errorf("catalog audio frames %d disagree with fixture", n))
	}
	pcm := fixture.AudioSamples(0, seq)
	sum := sha256.Sum256(int16Bytes(pcm))
	r.track.Samples = append(r.track.Samples, model.Sample{
		Seq: seq, Tick: tick, Audio: pcm,
		PayloadHash: fmt.Sprintf("%x", sum[:8]),
	})
	return nil
}

func (r *mp4Reader) readEventEntry(pr *prefixReader, seq uint32, tick clock.Tick) error {
	n, err := pr.u32()
	if err != nil {
		return err
	}
	keyb, err := pr.bytes(int(n))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(keyb)
	r.track.Events = append(r.track.Events, model.Event{
		Key:          string(keyb),
		Track:        r.track.ID,
		Kind:         r.track.Kind,
		Seq:          seq,
		ObservedTick: tick,
		Delay:        tick,
		PayloadHash:  fmt.Sprintf("%x", sum[:8]),
	})
	return nil
}

// prefixReader walks one smpc payload and turns short reads into locatable
// truncation errors carrying the absolute file offset.
type prefixReader struct {
	data  []byte
	pos   uint64
	base  uint64
	path  string
	track string
}

func (p *prefixReader) offset() int64 { return int64(p.base + p.pos) }

func (p *prefixReader) bytes(n int) ([]byte, error) {
	if int(p.pos)+n > len(p.data) {
		return nil, model.NewError(model.StageParse, "truncated-catalog", p.path,
			p.track, p.offset(),
			fmt.Errorf("%w: wanted %d bytes, %d left", model.ErrTruncated, n,
				len(p.data)-int(p.pos)))
	}
	b := p.data[p.pos : p.pos+uint64(n)]
	p.pos += uint64(n)
	return b, nil
}

func (p *prefixReader) readByte() (byte, error) {
	b, err := p.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (p *prefixReader) u32() (uint32, error) {
	b, err := p.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (p *prefixReader) u64() (uint64, error) {
	b, err := p.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func int16Bytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}
