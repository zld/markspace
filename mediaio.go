package mediacore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"
)

func sha256Sum(r io.Reader) [32]byte {
	h := sha256.New()
	_, _ = io.Copy(h, r)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// newMP4Writer opens a box, marshals payload (may be nil for containers) and
// closes it; the size header is patched automatically by EndBox.
func box(w *mp4.Writer, typ mp4.BoxType, payload mp4.IImmutableBox, body func() error) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if payload != nil {
		if _, err := mp4.Marshal(w, payload, mp4.Context{}); err != nil {
			return err
		}
	}
	if body != nil {
		if err := body(); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

// FragmentSpec describes one generated fMP4 fragment.
type FragmentSpec struct {
	TrackID      uint32
	Timescale    uint32
	BaseDecode   uint64 // tfdt baseMediaDecodeTime in timescale ticks
	SampleDeltas []uint32
	Payload      []byte
	Color        *ColorInfo // nil -> no colr box
}

// BuildFragment renders a standalone fMP4 fragment (styp+moof+mdat). The moof
// carries tfhd/tfdt/trun and an optional colr box so InspectSegment can parse
// timing, samples and color metadata back through go-mp4.
func BuildFragment(spec FragmentSpec) ([]byte, error) {
	var buf bytes.Buffer
	w := mp4.NewWriter(newWriteSeeker(&buf))
	if err := box(w, mp4.BoxTypeStyp(), &mp4.Styp{
		MajorBrand: [4]byte{'m', 's', 'i', 'd'},
	}, nil); err != nil {
		return nil, err
	}
	var totalDelta uint32
	for _, d := range spec.SampleDeltas {
		totalDelta += d
	}
	err := box(w, mp4.BoxTypeMoof(), nil, func() error {
		if err := box(w, mp4.BoxTypeMfhd(), &mp4.Mfhd{SequenceNumber: 1}, nil); err != nil {
			return err
		}
		return box(w, mp4.BoxTypeTraf(), nil, func() error {
			if err := box(w, mp4.BoxTypeTfhd(), &mp4.Tfhd{TrackID: spec.TrackID}, nil); err != nil {
				return err
			}
			tfdt := &mp4.Tfdt{FullBox: mp4.FullBox{Version: 1},
				BaseMediaDecodeTimeV1: spec.BaseDecode}
			if err := box(w, mp4.BoxTypeTfdt(), tfdt, nil); err != nil {
				return err
			}
			if spec.Color != nil && spec.Color.Present {
				colr := &mp4.Colr{
					ColourType:              [4]byte{'n', 'c', 'l', 'x'},
					ColourPrimaries:         spec.Color.Primaries,
					TransferCharacteristics: spec.Color.Transfer,
					MatrixCoefficients:      spec.Color.Matrix,
					FullRangeFlag:           spec.Color.FullRange,
				}
				if err := box(w, mp4.BoxTypeColr(), colr, nil); err != nil {
					return err
				}
			}
			// trun flag 0x000100 = sample-duration-present.
			trun := &mp4.Trun{
				FullBox:     mp4.FullBox{Flags: [3]byte{0x00, 0x01, 0x00}},
				SampleCount: uint32(len(spec.SampleDeltas)),
			}
			for _, d := range spec.SampleDeltas {
				trun.Entries = append(trun.Entries, mp4.TrunEntry{SampleDuration: d,
					SampleSize: uint32(len(spec.Payload) / max1(len(spec.SampleDeltas)))})
			}
			return box(w, mp4.BoxTypeTrun(), trun, nil)
		})
	})
	if err != nil {
		return nil, err
	}
	if err := box(w, mp4.BoxTypeMdat(), &mp4.Mdat{Data: spec.Payload}, nil); err != nil {
		return nil, err
	}
	_ = totalDelta
	return buf.Bytes(), nil
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker; seeking is only used to
// patch box size headers and always lands inside the buffer.
type writeSeeker struct {
	b   *bytes.Buffer
	pos int64
}

func newWriteSeeker(b *bytes.Buffer) *writeSeeker { return &writeSeeker{b: b} }

func (ws *writeSeeker) Write(p []byte) (int, error) {
	end := ws.pos + int64(len(p))
	if end > int64(ws.b.Len()) {
		ws.b.Grow(int(end - int64(ws.b.Len())))
		ws.b.Write(make([]byte, end-int64(ws.b.Len())))
	}
	copy(ws.b.Bytes()[ws.pos:end], p)
	ws.pos = end
	return len(p), nil
}

func (ws *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		ws.pos = offset
	case io.SeekCurrent:
		ws.pos += offset
	case io.SeekEnd:
		ws.pos = int64(ws.b.Len()) + offset
	}
	return ws.pos, nil
}

// BuildRTPCapture serializes packets as 2-byte-length-prefixed records, the
// format InspectRTPStream consumes.
func BuildRTPCapture(pkts []*rtp.Packet) ([]byte, error) {
	var buf bytes.Buffer
	for _, p := range pkts {
		raw, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		if len(raw) > 0xffff {
			return nil, WrapMsg(ErrOverflow, "", 0, StageSegment, -1, "RTP packet too large")
		}
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
		buf.Write(hdr[:])
		buf.Write(raw)
	}
	return buf.Bytes(), nil
}

// NewRTPPacket builds one minimal RTP packet with the given sequence/timestamp.
func NewRTPPacket(ssrc uint32, seq uint16, ts uint32, payload byte) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96,
		SequenceNumber: seq, Timestamp: ts, SSRC: ssrc, Marker: true},
		Payload: []byte{payload}}
}

// hashWriter accumulates a running SHA-256 while bytes stream through.
type hashWriter struct{ h hash.Hash }

func (hw *hashWriter) Write(p []byte) (int, error) { return hw.h.Write(p) }
