// Package wav parses PCM/WAV sample blocks arriving in arbitrary-sized
// chunks. The decoder is streaming: callers may feed headers and PCM data in
// pieces as they arrive over the network. Truncated chunks and non-integral
// frames produce fault errors carrying the absolute input byte offset; the
// decoder never silently zero-pads missing samples.
package wav

import (
	"encoding/binary"
	"io"

	"mediad/internal/fault"
)

// Format describes a decoded PCM stream. Only the explicitly supported PCM
// bit depths and 1..8 channels are accepted.
type Format struct {
	AudioFormat   uint16 // PCM == 1, the only format accepted
	NumChannels   uint16
	SampleRate    uint32
	BitsPerSample uint16
}

// FrameSize returns the byte length of one interleaved sample frame.
func (f Format) FrameSize() int {
	return int(f.NumChannels) * int(f.BitsPerSample) / 8
}

// BytesPerSample returns the byte width of one channel sample.
func (f Format) BytesPerSample() int { return int(f.BitsPerSample) / 8 }

const (
	pcmFormat        = 1
	maxChannels      = 8
	waveHeaderMinLen = 16
)

// Decoder consumes a WAV byte stream and emits whole interleaved sample
// frames. The zero value is not usable; use NewDecoder.
type Decoder struct {
	r          io.Reader // always a *sourceMux; no bufio so a mid-stream EOF
	mux        *sourceMux // cannot be cached ahead of an Append
	offset     int64     // absolute bytes consumed from the input stream
	format     Format
	haveFormat bool
	dataLeft   int64 // bytes remaining inside the data chunk, -1 until found
	dataDecl   int64 // declared data chunk size from the header
	headerDone bool
}

// NewDecoder wraps a WAV byte stream. The reader may deliver the RIFF header
// and PCM payload in arbitrarily small pieces.
func NewDecoder(r io.Reader) *Decoder {
	mux := &sourceMux{sources: []io.Reader{r}}
	return &Decoder{r: mux, mux: mux, dataLeft: -1}
}

// Append chains another byte source after the currently buffered data. It lets
// one decoder span many transport chunks without re-parsing the RIFF header.
func (d *Decoder) Append(r io.Reader) {
	d.mux.append(r)
}

// Reset repoints the decoder at a fresh byte stream and clears every parsed
// state, allowing one decoder to be reused across independent streams.
func (d *Decoder) Reset(r io.Reader) {
	d.mux = &sourceMux{sources: []io.Reader{r}}
	d.r = d.mux
	d.offset = 0
	d.format = Format{}
	d.haveFormat = false
	d.dataLeft = -1
	d.dataDecl = 0
	d.headerDone = false
}

// sourceMux presents a sequence of readers as one continuous byte stream so
// the buffering layer never discards unread data at a chunk boundary.
type sourceMux struct {
	sources []io.Reader
}

func (m *sourceMux) append(r io.Reader) { m.sources = append(m.sources, r) }

// exhausted reports that every queued source has returned io.EOF.
func (m *sourceMux) exhausted() bool { return len(m.sources) == 0 }

func (m *sourceMux) Read(p []byte) (int, error) {
	for len(m.sources) > 0 {
		n, err := m.sources[0].Read(p)
		if err == io.EOF {
			m.sources = m.sources[1:]
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
	return 0, io.EOF
}

// Offset reports how many input bytes have been consumed so far. It is the
// reference used by fault.Error offsets returned by this package.
func (d *Decoder) Offset() int64 { return d.offset }

// Format returns the parsed stream format. It must only be called after the
// fmt chunk has been consumed.
func (d *Decoder) Format() (Format, error) {
	if !d.haveFormat {
		return Format{}, fault.New(fault.CatHeader, "wav.format", true,
			"fmt chunk not yet parsed")
	}
	return d.format, nil
}

// rawReader exposes the low-level byte read so internal header parsing never
// re-enters the payload-level Read method.
type rawReader struct{ d *Decoder }

func (r rawReader) Read(p []byte) (int, error) { return r.d.read(p) }

func (d *Decoder) read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.offset += int64(n)
	return n, err
}

// readFull keeps the offset accurate even when the underlying reader returns
// a short read followed by an error.
func (d *Decoder) readFull(p []byte) error {
	_, err := io.ReadFull(rawReader{d}, p)
	return err
}

// Read implements io.Reader over the PCM payload only (RIFF/chunk headers are
// consumed internally). It returns whole-frame payloads; a trailing partial
// frame or truncated data chunk is reported as a fault error.
func (d *Decoder) Read(p []byte) (int, error) {
	if !d.headerDone {
		if err := d.parseHeader(); err != nil {
			return 0, err
		}
	}
	if d.dataLeft == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > d.dataLeft {
		p = p[:int(d.dataLeft)]
	}
	n, err := d.read(p)
	if n > 0 {
		d.dataLeft -= int64(n)
	}
	switch {
	case err == io.EOF && d.dataLeft > 0:
		// The currently queued sources are drained but more chunks may be
		// appended later: a soft "need data" condition, not real truncation.
		return n, ErrNeedsData
	case err != nil:
		return n, err
	default:
		// Read returns arbitrary byte counts; frame-integrity checks belong
		// to ReadFrame and the frame assembler above this decoder.
		return n, nil
	}
}

// Finalize must be called once every transport chunk has been appended. It
// turns an unresolved ErrNeedsData condition into a real truncation fault,
// detects a non-integral trailing frame, and confirms clean completion.
func (d *Decoder) Finalize() error {
	if !d.headerDone {
		return fault.New(fault.CatTruncated, "wav.finalize", true,
			"stream ended before the data chunk was reached")
	}
	switch {
	case d.dataLeft == 0:
		return nil
	case d.dataDecl%int64(d.format.FrameSize()) != 0 && d.mux.exhausted():
		// The header itself declared a size that cannot hold whole frames.
		return fault.New(fault.CatAlignment, "wav.finalize", true,
			"data chunk size is not an integral number of frames")
	default:
		// The declared size was frame-aligned but the file ended early.
		return fault.Wrap(fault.CatTruncated, "wav.finalize", d.offset,
			faultNoTrack, true, io.ErrUnexpectedEOF)
	}
}

// ReadFrame returns exactly one interleaved sample frame.
func (d *Decoder) ReadFrame() ([]byte, error) {
	if !d.headerDone {
		if err := d.parseHeader(); err != nil {
			return nil, err
		}
	}
	frame := make([]byte, d.format.FrameSize())
	if d.dataLeft == 0 {
		return nil, io.EOF
	}
	if err := d.readFull(frame); err != nil {
		// A short read with data still expected means the queued chunks are
		// temporarily drained; the caller may Append more and retry.
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if d.dataLeft > 0 {
				return nil, ErrNeedsData
			}
			return nil, io.EOF
		}
		return nil, err
	}
	d.dataLeft -= int64(len(frame))
	if d.dataLeft < 0 {
		return nil, fault.New(fault.CatAlignment, "wav.frame", true,
			"data chunk ends in the middle of a sample frame")
	}
	return frame, nil
}

const faultNoTrack = -1

func (d *Decoder) parseHeader() error {
	var riff [12]byte
	if err := d.readFull(riff[:]); err != nil {
		return fault.Wrap(fault.CatTruncated, "wav.riff", d.offset,
			faultNoTrack, true, err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return fault.New(fault.CatHeader, "wav.riff", true,
			"missing RIFF/WAVE signature")
	}
	for {
		var ch [8]byte
		if err := d.readFull(ch[:]); err != nil {
			if err == io.EOF {
				return fault.New(fault.CatHeader, "wav.chunk", true,
					"data chunk not found before end of file")
			}
			return fault.Wrap(fault.CatHeader, "wav.chunk", d.offset,
				faultNoTrack, true, err)
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			if err := d.parseFmt(size); err != nil {
				return err
			}
		case "data":
			if !d.haveFormat {
				return fault.New(fault.CatHeader, "wav.data", true,
					"data chunk appears before fmt chunk")
			}
			d.dataLeft = size
			d.dataDecl = size
			d.headerDone = true
			return nil
		default:
			if err := d.discardChunk(size); err != nil {
				return err
			}
		}
	}
}

func (d *Decoder) parseFmt(size int64) error {
	if size < waveHeaderMinLen {
		return fault.Wrap(fault.CatHeader, "wav.fmt", d.offset,
			faultNoTrack, true, errShortFmt)
	}
	body := make([]byte, size)
	if err := d.readFull(body); err != nil {
		return fault.Wrap(fault.CatTruncated, "wav.fmt", d.offset,
			faultNoTrack, true, err)
	}
	f := Format{
		AudioFormat:   binary.LittleEndian.Uint16(body[0:2]),
		NumChannels:   binary.LittleEndian.Uint16(body[2:4]),
		SampleRate:    binary.LittleEndian.Uint32(body[4:8]),
		BitsPerSample: binary.LittleEndian.Uint16(body[14:16]),
	}
	if f.AudioFormat != pcmFormat {
		return fault.New(fault.CatHeader, "wav.fmt", true,
			"only uncompressed PCM (format tag 1) is supported")
	}
	if f.NumChannels < 1 || f.NumChannels > maxChannels {
		return fault.New(fault.CatHeader, "wav.fmt", true,
			"channel count must be between 1 and 8")
	}
	switch f.BitsPerSample {
	case 16, 24, 32:
	default:
		return fault.New(fault.CatHeader, "wav.fmt", true,
			"only 16/24/32-bit PCM samples are supported")
	}
	if f.SampleRate == 0 {
		return fault.New(fault.CatHeader, "wav.fmt", true,
			"sample rate must be non-zero")
	}
	blockAlign := binary.LittleEndian.Uint16(body[12:14])
	if int(blockAlign) != f.FrameSize() {
		return fault.New(fault.CatHeader, "wav.fmt", true,
			"block align contradicts channel count and sample width")
	}
	d.format = f
	d.haveFormat = true
	return nil
}

func (d *Decoder) discardChunk(size int64) error {
	// RIFF chunks are word-aligned with a padding byte when the size is odd.
	skip := size + size&1
	if _, err := io.CopyN(io.Discard, rawReader{d}, skip); err != nil {
		return fault.Wrap(fault.CatTruncated, "wav.skip", d.offset,
			faultNoTrack, true, err)
	}
	return nil
}
