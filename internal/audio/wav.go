package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"mediad/internal/mederr"
)

// EncodeWAV16 encodes planar float PCM as interleaved signed 16-bit WAV.
// Samples are clamped before quantization; silence remains all zero.
func EncodeWAV16(b *Buffer) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	frames := b.Frames()
	ch := b.Layout.Channels
	dataBytes := frames * ch * 2
	out := make([]byte, 44+dataBytes)
	copy(out[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+dataBytes))
	copy(out[8:12], []byte("WAVE"))
	copy(out[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:24], uint16(ch))
	binary.LittleEndian.PutUint32(out[24:28], uint32(b.Rate))
	byteRate := b.Rate * ch * 2
	binary.LittleEndian.PutUint32(out[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(out[32:34], uint16(ch*2))
	binary.LittleEndian.PutUint16(out[34:36], 16)
	copy(out[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(out[40:44], uint32(dataBytes))

	pos := 44
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			v := b.Planar[c][i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			v = math.Max(-1, math.Min(1, v))
			q := int16(math.Round(v * 32767))
			binary.LittleEndian.PutUint16(out[pos:pos+2], uint16(q))
			pos += 2
		}
	}
	return out, nil
}

// WriteWAV16 writes a WAV file atomically by the caller's chunk convention.
func WriteWAV16(path string, b *Buffer) error {
	data, err := EncodeWAV16(b)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return mederr.New(mederr.CatIO, "wav", -1, -1, false, "write %s: %v", path, err)
	}
	return nil
}

// WAVInfo is the small subset of WAV metadata needed for chunk verification.
type WAVInfo struct {
	AudioFormat uint16
	Channels    uint16
	Rate        uint32
	Bits        uint16
	DataBytes   uint32
}

// VerifyWAV checks the RIFF header, declared data size and expected PCM shape.
// It catches zero-byte and truncated chunk outputs before checkpoint commit.
func VerifyWAV(path string, wantRate, wantChannels, wantFrames int) (WAVInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return WAVInfo{}, err
	}
	defer f.Close()
	var hdr [44]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return WAVInfo{}, fmt.Errorf("short WAV header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" || string(hdr[12:16]) != "fmt " {
		return WAVInfo{}, fmt.Errorf("not a PCM WAV file")
	}
	info := WAVInfo{
		AudioFormat: binary.LittleEndian.Uint16(hdr[20:22]),
		Channels:    binary.LittleEndian.Uint16(hdr[22:24]),
		Rate:        binary.LittleEndian.Uint32(hdr[24:28]),
		Bits:        binary.LittleEndian.Uint16(hdr[34:36]),
		DataBytes:   binary.LittleEndian.Uint32(hdr[40:44]),
	}
	if info.AudioFormat != 1 || info.Bits != 16 || int(info.Rate) != wantRate || int(info.Channels) != wantChannels {
		return info, fmt.Errorf("unsupported WAV format %+v", info)
	}
	wantData := uint32(wantFrames * wantChannels * 2)
	if info.DataBytes != wantData {
		return info, fmt.Errorf("data size %d != expected %d", info.DataBytes, wantData)
	}
	sz, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return info, err
	}
	if want := int64(44) + int64(wantData); sz != want {
		return info, fmt.Errorf("file size %d != expected %d", sz, want)
	}
	return info, nil
}

// DecodeWAV16 decodes the project's standard PCM WAV output into planar float.
func DecodeWAV16(path string) (*Buffer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "wav", -1, -1, false, "read WAV: %v", err)
	}
	if len(data) < 44 {
		return nil, mederr.New(mederr.CatParse, "wav", -1, -1, true, "truncated WAV header")
	}
	ch := int(binary.LittleEndian.Uint16(data[22:24]))
	rate := int(binary.LittleEndian.Uint32(data[24:28]))
	dataBytes := int(binary.LittleEndian.Uint32(data[40:44]))
	if ch <= 0 || ch > 8 || rate <= 0 || 44+dataBytes > len(data) {
		return nil, mederr.New(mederr.CatParse, "wav", -1, -1, true, "invalid/truncated WAV metadata")
	}
	frames := dataBytes / (ch * 2)
	layout, err := ParseLayout(fmt.Sprint(ch))
	if err != nil {
		// ParseLayout only names common layouts; arbitrary bounded channel
		// counts are still representable for verification/measurement.
		layout = Layout{Name: fmt.Sprintf("%d.0", ch), Channels: ch}
	}
	b, err := NewBuffer(rate, layout, frames)
	if err != nil {
		return nil, err
	}
	pos := 44
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			u := binary.LittleEndian.Uint16(data[pos : pos+2])
			b.Planar[c][i] = float64(int16(u)) / 32768.0
			pos += 2
		}
	}
	return b, nil
}
