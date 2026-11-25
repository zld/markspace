package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

var (
	errShortFmt = errors.New("fmt chunk shorter than 16 bytes")
)

// Encode builds a canonical little-endian PCM WAV byte stream containing pcm
// as interleaved sample frames. It is used by tests and the demo command to
// synthesize fixtures locally; no external media files are needed.
func Encode(f Format, pcm []byte) ([]byte, error) {
	if f.AudioFormat != pcmFormat {
		return nil, errors.New("Encode: only PCM format tag 1 is supported")
	}
	var buf bytes.Buffer
	dataLen := uint32(len(pcm))
	riffSize := uint32(4) + (8 + waveHeaderMinLen) + (8 + dataLen)

	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, riffSize)
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(waveHeaderMinLen))
	_ = binary.Write(&buf, binary.LittleEndian, f.AudioFormat)
	_ = binary.Write(&buf, binary.LittleEndian, f.NumChannels)
	_ = binary.Write(&buf, binary.LittleEndian, f.SampleRate)
	byteRate := f.SampleRate * uint32(f.FrameSize())
	_ = binary.Write(&buf, binary.LittleEndian, byteRate)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(f.FrameSize()))
	_ = binary.Write(&buf, binary.LittleEndian, f.BitsPerSample)

	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataLen)
	buf.Write(pcm)
	return buf.Bytes(), nil
}

// ReadAllPCM drains the decoder and returns the full PCM payload. It fails
// with a fault error if the stream ends on a non-integral frame boundary.
func ReadAllPCM(d *Decoder) ([]byte, Format, error) {
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := d.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Format{}, err
		}
	}
	f, err := d.Format()
	if err != nil {
		return nil, Format{}, err
	}
	if out.Len()%f.FrameSize() != 0 {
		return nil, f, errors.New("wav: payload length is not an integral number of frames")
	}
	return out.Bytes(), f, nil
}
