package demux

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// WriteFixture renders a deterministic MP4 container for the spec. All sample
// data is synthetic fixed-fill bytes; only structural and timing metadata
// carry meaning for the kernel. Faults are injected at precise points so the
// same fault propagates through demux, timeline and manifest stages.
func WriteFixture(spec FixtureSpec) ([]byte, error) {
	if !ValidFault(spec.Fault) {
		return nil, fmt.Errorf("unknown fault case %q", spec.Fault)
	}
	ws := &writeSeeker{}
	w := mp4.NewWriter(ws)
	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	videoData, audioData := buildMdatPayload(spec)
	if err := writeMoov(w, spec, uint64(len(videoData)), uint64(len(audioData))); err != nil {
		return nil, err
	}
	if err := writeMdat(w, videoData, audioData); err != nil {
		return nil, err
	}
	out := ws.bytes()
	if spec.Fault == FaultTruncatedIndex {
		out = injectTruncation(out)
	}
	return out, nil
}

// writeSeeker is a random-access in-memory io.WriteSeeker. bytes.Buffer cannot
// rewrite earlier positions (its Write only appends), which would break the
// box-size back-patching performed by EndBox; a growing byte slice can.
type writeSeeker struct {
	mem []byte
	off int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.off + int64(len(p))
	if end > int64(len(w.mem)) {
		w.mem = append(w.mem, make([]byte, end-int64(len(w.mem)))...)
	}
	copy(w.mem[w.off:end], p)
	w.off = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = w.off + offset
	case io.SeekEnd:
		target = int64(len(w.mem)) + offset
	}
	if target < 0 || target > int64(len(w.mem)) {
		return 0, fmt.Errorf("seek %d out of range [0,%d]", target, len(w.mem))
	}
	w.off = target
	return target, nil
}

func (w *writeSeeker) bytes() []byte { return w.mem }

func writeFtyp(w *mp4.Writer) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()})
	if err != nil {
		return err
	}
	_, err = mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, bi.Context)
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeMdat(w *mp4.Writer, videoData, audioData []byte) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return err
	}
	all := append(append([]byte{}, videoData...), audioData...)
	if _, err := mp4.Marshal(w, &mp4.Mdat{Data: all}, bi.Context); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

// buildMdatPayload creates one fixed-fill byte block per sample. Sizes are
// deterministic functions of seed and sample index.
func buildMdatPayload(spec FixtureSpec) (video []byte, audio []byte) {
	video = make([]byte, int(spec.Video.FrameCount)*64)
	for i := range video {
		video[i] = byte((spec.Seed + int64(i)) % 251)
	}
	audio = make([]byte, int(spec.Audio.FrameCount)*32)
	for i := range audio {
		audio[i] = byte((spec.Seed*7 + int64(i)) % 251)
	}
	return video, audio
}

// mdstPayload renders the custom metadata box payload.
func mdstPayload(kind byte, layout uint8, rate uint16, colorSrc byte, cp uint16) []byte {
	p := make([]byte, 8)
	p[0] = 0 // version
	p[1] = layout
	binary.BigEndian.PutUint16(p[2:4], rate)
	p[4] = colorSrc
	// p[5] reserved
	binary.BigEndian.PutUint16(p[6:8], cp)
	_ = kind
	return p
}
