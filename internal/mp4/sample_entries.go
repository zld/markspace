package mp4mux

import (
	"github.com/abema/go-mp4"
)

// writeVideoStsd emits an avc1 sample entry with a minimal avcC.
func writeVideoStsd(w *mp4.Writer, cfg Config) error {
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	vse := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           uint16(cfg.Width),
		Height:          uint16(cfg.Height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, vse, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvcC()}); err != nil {
		return err
	}
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		Profile:            0x64,
		Level:              0x1F,
		LengthSizeMinusOne: 3,
	}
	if _, err := mp4.Marshal(w, avcc, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // avcC
		return err
	}
	if _, err := w.EndBox(); err != nil { // avc1
		return err
	}
	_, err := w.EndBox() // stsd
	return err
}

// writeAudioStsd emits an Opus sample entry with its dOps configuration.
func writeAudioStsd(w *mp4.Writer, rate uint32) error {
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	ase := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: 2,
		SampleSize:   16,
		SampleRate:   rate << 16,
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, ase, mp4.Context{}); err != nil {
		return err
	}
	dops := &mp4.DOps{
		Version:            0,
		OutputChannelCount: 2,
		PreSkip:            312,
		InputSampleRate:    rate,
	}
	if err := writeBox(w, dops, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // Opus
		return err
	}
	_, err := w.EndBox() // stsd
	return err
}
