package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"continuity/internal/media"
)

// buildWAV assembles a minimal PCM WAV with optional trailing junk chunks.
func buildWAV(sampleRate uint32, channels, bits uint16, frames int, extra string) []byte {
	blockAlign := channels * bits / 8
	dataLen := uint32(frames * int(blockAlign))
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+int(dataLen)+len(extra)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, channels)
	binary.Write(&b, binary.LittleEndian, sampleRate)
	binary.Write(&b, binary.LittleEndian, sampleRate*uint32(blockAlign))
	binary.Write(&b, binary.LittleEndian, blockAlign)
	binary.Write(&b, binary.LittleEndian, bits)
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataLen)
	pcm := make([]byte, dataLen)
	for i := range pcm {
		pcm[i] = byte(i * 7)
	}
	b.Write(pcm)
	b.WriteString(extra)
	return b.Bytes()
}

func TestParseAndReadFrames(t *testing.T) {
	raw := buildWAV(48000, 2, 16, 100, "")
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.FrameCount != 100 || f.SampleRate != 48000 || f.Channels != 2 || f.BitsPerSample != 16 {
		t.Fatalf("unexpected header: %+v", f)
	}
	dst := make([]byte, 10*f.BlockAlign)
	n, err := f.ReadFrames(dst, 50)
	if err != nil || n != 10 {
		t.Fatalf("read frames: n=%d err=%v", n, err)
	}
	// Byte content must equal the generated PCM at the same offset.
	off := f.DataOffset + 50*int64(f.BlockAlign)
	if !bytes.Equal(dst, raw[off:off+int64(len(dst))]) {
		t.Fatal("frame bytes mismatch")
	}
	if _, err := f.ReadFrames(dst, f.FrameCount); err != io.EOF {
		t.Fatalf("expected EOF past end, got %v", err)
	}
}

func TestTruncatedHeader(t *testing.T) {
	raw := buildWAV(48000, 2, 16, 10, "")
	for _, cut := range []int{4, 11, 20, 35} {
		_, err := Parse(bytes.NewReader(raw[:cut]))
		if err == nil {
			t.Fatalf("cut=%d accepted", cut)
		}
		if media.CategoryOf(err) != media.CatInvalidInput {
			t.Fatalf("cut=%d category=%s", cut, media.CategoryOf(err))
		}
	}
	// Garbage magic.
	if _, err := Parse(bytes.NewReader([]byte("XXXX............"))); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestPartialFrameRejected(t *testing.T) {
	raw := buildWAV(48000, 2, 16, 10, "")
	// Shrink the declared data length by one byte via the header field.
	corrupt := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(corrupt[40:44], binary.LittleEndian.Uint32(corrupt[40:44])-1)
	_, err := Parse(bytes.NewReader(corrupt))
	if !errors.Is(err, ErrPartialFrame) {
		t.Fatalf("want ErrPartialFrame, got %v", err)
	}
}

func TestDeclaredDataExceedsFile(t *testing.T) {
	raw := buildWAV(48000, 1, 16, 8, "")
	corrupt := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(corrupt[40:44], 1000) // claim more than present
	_, err := Parse(bytes.NewReader(corrupt))
	if media.CategoryOf(err) != media.CatInvalidInput {
		t.Fatalf("want invalid input, got %v", err)
	}
}

func TestBlockAlignMismatch(t *testing.T) {
	raw := buildWAV(48000, 2, 16, 10, "")
	corrupt := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(corrupt[28:30], 3) // blockAlign should be 4
	_, err := Parse(bytes.NewReader(corrupt))
	if media.CategoryOf(err) != media.CatLayout {
		t.Fatalf("want layout error, got %v", err)
	}
}

func TestNonPCMRejected(t *testing.T) {
	raw := buildWAV(48000, 2, 16, 10, "")
	corrupt := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(corrupt[20:22], 3) // IEEE float tag
	_, err := Parse(bytes.NewReader(corrupt))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want unsupported, got %v", err)
	}
}

func TestErrorStringCarriesCategory(t *testing.T) {
	err := media.WrapError(media.CatInvalidInput, "x", io.ErrUnexpectedEOF)
	if !strings.Contains(err.Error(), string(media.CatInvalidInput)) {
		t.Fatalf("error string missing category: %q", err.Error())
	}
}
