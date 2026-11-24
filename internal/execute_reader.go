package medpipe

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"
)

// FrameReader performs random-access reads of complete frames from a capture
// using the rebuilt index. It seeks to an entry's byte offset and then
// reassembles exactly the RTP records belonging to that frame, so the
// streaming stage never scans the whole file when resuming.
type FrameReader struct {
	f     *os.File
	r     *bufio.Reader
	idx   *TrackIndex
	track string
	b     *ErrorBuilder
	cur   int // ordinal of the next frame to read
}

// NewFrameReader opens the capture and validates the magic header before any
// indexed seek is attempted.
func NewFrameReader(capturePath string, idx *TrackIndex, track string) (*FrameReader, error) {
	if err := RequireLocalFile(capturePath); err != nil {
		return nil, err
	}
	f, err := os.Open(capturePath)
	if err != nil {
		return nil, err
	}
	if errs := VerifyMagic(f, track); len(errs) > 0 {
		f.Close()
		return nil, errs[0]
	}
	return &FrameReader{
		f:     f,
		r:     bufio.NewReaderSize(f, 64*1024),
		idx:   idx,
		track: track,
		b:     NewErrorBuilder(track),
	}, nil
}

// SeekFrame positions the reader at the indexed frame ordinal. Seeking is
// absolute against the underlying file, so buffered data is discarded.
func (fr *FrameReader) SeekFrame(ordinal int) error {
	if ordinal < 0 || ordinal >= len(fr.idx.Entries) {
		return fmt.Errorf("frame ordinal %d out of range [0,%d)", ordinal, len(fr.idx.Entries))
	}
	off := fr.idx.Entries[ordinal].ByteOffset
	if _, err := fr.f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	fr.r.Reset(fr.f)
	fr.cur = ordinal
	return nil
}

// ReadChunk returns up to n complete frames starting at the current position.
func (fr *FrameReader) ReadChunk(n int) ([]Frame, error) {
	out := make([]Frame, 0, n)
	for len(out) < n && fr.cur < len(fr.idx.Entries) {
		f, err := fr.readOne()
		if err != nil {
			return out, err
		}
		out = append(out, f)
		fr.cur++
	}
	return out, nil
}

// readOne reassembles one frame from the records at the current offset. All
// records sharing the frame's RTP timestamp belong to it; the marker bit
// confirms the final record.
func (fr *FrameReader) readOne() (Frame, error) {
	entry := fr.idx.Entries[fr.cur]
	var payloads [][]byte
	var frameTS uint32
	first := true
	for {
		pkt, off, err := fr.readRecord()
		if err != nil {
			return Frame{}, err
		}
		if first {
			frameTS = pkt.Timestamp
			first = false
		} else if pkt.Timestamp != frameTS {
			// A timestamp boundary without a marker means the index/stream
			// disagree; fail rather than merge two frames silently.
			return Frame{}, fr.b.FatalAt(CodeInvalidRTP,
				"frame boundary without RTP marker during indexed read",
				"rebuild the index; the capture layout changed", off)
		}
		payloads = append(payloads, pkt.Payload)
		if pkt.Marker {
			break
		}
	}
	f := Frame{
		Codec:      fr.idx.Codec,
		Keyframe:   entry.Keyframe,
		Ordinal:    entry.Ordinal,
		ByteOffset: entry.ByteOffset,
		RawPayload: joinPayloads(payloads),
		Meta:       entry.Meta,
		HasMeta:    entry.Meta != FrameMeta{},
	}
	return f, nil
}

// readRecord reads one length-prefixed RTP record at the current file position.
func (fr *FrameReader) readRecord() (*rtp.Packet, int64, error) {
	off, err := fr.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, 0, err
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(fr.r, lenBuf[:]); err != nil {
		return nil, off, err
	}
	length := int(binary.BigEndian.Uint16(lenBuf[:]))
	if length == 0 || length > maxRecordSize {
		return nil, off, fr.b.FatalAt(CodeInvalidRTP,
			fmt.Sprintf("illegal record length %d during indexed read", length),
			"the capture changed after indexing; rebuild the index", off)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(fr.r, buf); err != nil {
		return nil, off, err
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, off, fr.b.RecoverableAt(CodeInvalidRTP, err.Error(),
			"drop the malformed packet at capture time", off)
	}
	return pkt, off, nil
}

// Close releases the file handle.
func (fr *FrameReader) Close() error { return fr.f.Close() }

func joinPayloads(parts [][]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
