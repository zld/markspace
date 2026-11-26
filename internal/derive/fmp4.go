// Package derive creates and rechecks deterministic fMP4 PCM fragments.
package derive

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"

	"github.com/abema/go-mp4"

	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
	"mediacheck/internal/validate"
)

// Segment is a validated fMP4 fragment and its accounting coordinates.
type Segment struct {
	View         validate.FragmentView `json:"view"`
	Data         []byte                `json:"-"`
	SHA256       string                `json:"fragment_sha256"`
	SourceOffset int64                 `json:"source_offset"`
	SourceLength int64                 `json:"source_length_bytes"`
}

// BuildOptions controls output bit depth and MP4 sequence number.
type BuildOptions struct {
	OutputBits int
	Sequence   uint32
}

type seekBuffer struct {
	b   []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.b)) {
		grown := make([]byte, end)
		copy(grown, s.b)
		s.b = grown
	}
	copy(s.b[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = int64(len(s.b)) + offset
	}
	if s.pos < 0 {
		return 0, timebase.NewError(timebase.CodeMP4, "negative seek position")
	}
	return s.pos, nil
}

func collectSamples(tr *track.Track, start, frames int64) ([]int32, error) {
	end := start + frames
	out := make([]int32, frames*int64(tr.Channels))
	copied := int64(0)
	for _, c := range tr.Chunks {
		cs := max64(c.StartSample, start)
		ce := min64(c.EndSample(), end)
		if cs >= ce {
			continue
		}
		srcFrom := (cs - c.StartSample) * int64(tr.Channels)
		dstFrom := (cs - start) * int64(tr.Channels)
		n := (ce - cs) * int64(tr.Channels)
		if srcFrom < 0 || srcFrom+n > int64(len(c.Samples)) || dstFrom+n > int64(len(out)) {
			return nil, timebase.NewError(timebase.CodeFragment, "sample copy range out of bounds")
		}
		copy(out[dstFrom:dstFrom+n], c.Samples[srcFrom:srcFrom+n])
		copied += n
	}
	if copied != int64(len(out)) {
		return nil, timebase.NewError(timebase.CodeFragment, "fragment is missing %d samples", int64(len(out))-copied)
	}
	return out, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func encodePCM(samples []int32, bits, channels int) ([]byte, error) {
	frameBytes := channels * (bits / 8)
	if len(samples)%channels != 0 {
		return nil, timebase.NewError(timebase.CodeAlignment, "samples not frame aligned")
	}
	out := make([]byte, (len(samples)/channels)*frameBytes)
	for i, s := range samples {
		switch bits {
		case 16:
			v := int16(s >> 16)
			binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
		case 24:
			v := int32(s >> 8)
			u := uint32(v)
			out[i*3] = byte(u)
			out[i*3+1] = byte(u >> 8)
			out[i*3+2] = byte(u >> 16)
		default:
			return nil, timebase.NewError(timebase.CodeFragment, "unsupported output bits %d", bits)
		}
	}
	return out, nil
}

func writeBox(w *mp4.Writer, typ mp4.BoxType, payload []byte) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return timebase.NewError(timebase.CodeMP4, "start %s: %v", typ, err).Wrap(err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return timebase.NewError(timebase.CodeMP4, "write %s payload", typ).Wrap(err)
		}
	}
	if _, err := w.EndBox(); err != nil {
		return timebase.NewError(timebase.CodeMP4, "end %s: %v", typ, err).Wrap(err)
	}
	return nil
}

func encodeFMP4(seq uint32, startTick uint64, payload []byte, frameCount uint32, trackID uint32) ([]byte, error) {
	var sb seekBuffer
	w := mp4.NewWriter(&sb)

	styp := []byte("msdh")
	styp = binary.BigEndian.AppendUint32(styp, 0)
	styp = append(styp, "msdh"...)
	styp = append(styp, "msix"...)
	if err := writeBox(w, mp4.BoxTypeStyp(), styp); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return nil, timebase.NewError(timebase.CodeMP4, "start moof").Wrap(err)
	}
	mfhd := []byte{0, 0, 0, 0}
	mfhd = binary.BigEndian.AppendUint32(mfhd, seq)
	if err := writeBox(w, mp4.BoxTypeMfhd(), mfhd); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return nil, timebase.NewError(timebase.CodeMP4, "start traf").Wrap(err)
	}
	tfhd := []byte{0, 0, 0, 0}
	tfhd = binary.BigEndian.AppendUint32(tfhd, trackID)
	if err := writeBox(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
		return nil, err
	}
	tfdt := []byte{1, 0, 0, 0} // version 1: uint64 baseMediaDecodeTime
	tfdt = binary.BigEndian.AppendUint64(tfdt, startTick)
	if err := writeBox(w, mp4.BoxTypeTfdt(), tfdt); err != nil {
		return nil, err
	}
	trun := []byte{0, 0, 0, 0} // version/flags: sample_count only
	trun = binary.BigEndian.AppendUint32(trun, frameCount)
	if err := writeBox(w, mp4.BoxTypeTrun(), trun); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return nil, timebase.NewError(timebase.CodeMP4, "end traf").Wrap(err)
	}
	if _, err := w.EndBox(); err != nil { // moof
		return nil, timebase.NewError(timebase.CodeMP4, "end moof").Wrap(err)
	}
	if err := writeBox(w, mp4.BoxTypeMdat(), payload); err != nil {
		return nil, err
	}
	return append([]byte(nil), sb.b...), nil
}

// ReadBoxes uses go-mp4 to parse top-level box structure.
func ReadBoxes(data []byte) ([]string, error) {
	var boxes []string
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (any, error) {
		boxes = append(boxes, h.BoxInfo.Type.String())
		if h.BoxInfo.Type == mp4.BoxTypeMoof() || h.BoxInfo.Type == mp4.BoxTypeTraf() {
			if _, err := h.Expand(); err != nil {
				return nil, timebase.NewError(timebase.CodeMP4, "expand %s: %v", h.BoxInfo.Type, err).Wrap(err)
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, timebase.NewError(timebase.CodeMP4, "go-mp4 read failed: %v", err).Wrap(err)
	}
	return boxes, nil
}

// BuildFragment encodes, validates and wraps one frame-aligned fragment.
func BuildFragment(tr *track.Track, startSample, frames, sourceOffset int64, sourceLength int64, opts BuildOptions) (*Segment, error) {
	if opts.OutputBits != 16 && opts.OutputBits != 24 {
		return nil, timebase.NewError(timebase.CodeFragment, "output bits must be 16 or 24")
	}
	samples, err := collectSamples(tr, startSample, frames)
	if err != nil {
		return nil, err
	}
	payload, err := encodePCM(samples, opts.OutputBits, tr.Channels)
	if err != nil {
		return nil, err
	}
	startTick, err := timebase.SamplesToTicks(startSample, uint32(tr.SampleRate), timebase.TickRate)
	if err != nil {
		return nil, err
	}
	endTick, err := timebase.SamplesToTicks(startSample+frames, uint32(tr.SampleRate), timebase.TickRate)
	if err != nil {
		return nil, err
	}
	view := validate.FragmentView{
		Index: int(opts.Sequence), TrackID: tr.ID, StartSample: startSample, FrameCount: frames,
		SampleRate: tr.SampleRate, Channels: tr.Channels, Bits: opts.OutputBits,
		StartTick: startTick, EndTick: endTick, PayloadBytes: len(payload),
	}
	if err := validate.FragmentAgainstTrack(view, tr); err != nil {
		return nil, err
	}
	data, err := encodeFMP4(opts.Sequence, uint64(startTick), payload, uint32(frames), 1)
	if err != nil {
		return nil, err
	}
	if _, err := ReadBoxes(data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &Segment{
		View: view, Data: data, SHA256: hex.EncodeToString(sum[:]),
		SourceOffset: sourceOffset, SourceLength: sourceLength,
	}, nil
}
