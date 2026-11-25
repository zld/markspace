package wav

import (
	"encoding/binary"
	"testing"

	"mediad/media"
)

// putU32 writes a little-endian uint32 into a 4-byte slice.
func putU32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// buildWAVE assembles a minimal 16-bit stereo 48kHz WAVE with payload n bytes.
func buildWAVE(n int) []byte {
	buf := make([]byte, riffHeaderLen+24+8+n)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(4+24+8+n))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 2) // channels
	binary.LittleEndian.PutUint32(buf[24:28], 48000)
	binary.LittleEndian.PutUint32(buf[28:32], 48000*2*2) // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], 4)         // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)        // bits per sample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(n))
	return buf
}

func TestParseChunksValid(t *testing.T) {
	buf := buildWAVE(16) // 4 stereo frames
	f := ParseChunks(buf)
	if len(f.Faults) != 0 {
		t.Fatalf("valid WAVE must have no faults, got %+v", f.Faults)
	}
	if f.Fmt == nil || f.Data == nil {
		t.Fatal("fmt and data chunks must be present")
	}
	if f.Fmt.SampleRate != 48000 || f.Fmt.Channels != 2 || f.Fmt.BitsPerSample != 16 {
		t.Fatalf("fmt fields wrong: %+v", f.Fmt)
	}
	if f.Data.DataStart != 44 || f.Data.DataEnd != 60 || f.Data.HeaderStart != 36 {
		t.Fatalf("data offsets wrong: %+v", f.Data)
	}
	if f.Limit != int64(len(buf)) {
		t.Fatalf("limit = %d, want %d", f.Limit, len(buf))
	}
}

func TestTruncatedRIFFHeader(t *testing.T) {
	buf := buildWAVE(16)[:7]
	f := ParseChunks(buf)
	if len(f.Faults) != 1 || f.Faults[0].Category != media.CatTruncatedHeader {
		t.Fatalf("want one truncated_header fault, got %+v", f.Faults)
	}
	if f.Faults[0].ByteStart != 0 || f.Faults[0].ByteEnd != 7 {
		t.Fatalf("truncation range = [%d,%d), want [0,7)", f.Faults[0].ByteStart, f.Faults[0].ByteEnd)
	}
}

func TestBadContainerIdentifiers(t *testing.T) {
	buf := buildWAVE(8)
	copy(buf[0:4], "RIFX")
	copy(buf[8:12], "WAXE")
	f := ParseChunks(buf)
	var cats []media.Category
	for _, flt := range f.Faults {
		cats = append(cats, flt.Category)
	}
	if len(cats) != 2 || cats[0] != media.CatBadContainer || cats[1] != media.CatBadContainer {
		t.Fatalf("want two bad_container faults, got %+v", f.Faults)
	}
	if f.Faults[1].ByteStart != 8 || f.Faults[1].ByteEnd != 12 {
		t.Fatalf("WAVE marker range = [%d,%d), want [8,12)", f.Faults[1].ByteStart, f.Faults[1].ByteEnd)
	}
}

func TestChunkOutOfBounds(t *testing.T) {
	buf := buildWAVE(100)
	// Declare a data payload of 500 while only 100 bytes exist.
	binary.LittleEndian.PutUint32(buf[40:44], 500)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(4+24+8+500))
	f := ParseChunks(buf)
	var oob *media.Fault
	for i := range f.Faults {
		if f.Faults[i].Category == media.CatChunkOutOfBounds {
			oob = &f.Faults[i]
		}
	}
	if oob == nil {
		t.Fatalf("out-of-bounds data chunk not detected: %+v", f.Faults)
	}
	if oob.ByteStart != 44 || oob.ByteEnd != 44+500 {
		t.Fatalf("oob range = [%d,%d), want [44,544)", oob.ByteStart, oob.ByteEnd)
	}
	if f.Data == nil || f.Data.Size != 500 {
		t.Fatal("data chunk must still be recorded with declared size")
	}
}

func TestTruncatedChunkHeader(t *testing.T) {
	// RIFF(12) + fmt(24) + 5 stray bytes: a chunk header needs 8 bytes, so
	// the walk must flag a truncated header anchored to those 5 bytes.
	buf := make([]byte, 41)
	copy(buf, buildWAVE(16)[:36])
	binary.LittleEndian.PutUint32(buf[4:8], uint32(41-8))
	for i := 36; i < 41; i++ {
		buf[i] = 0xDA
	}
	f := ParseChunks(buf)
	var th *media.Fault
	for i := range f.Faults {
		if f.Faults[i].Category == media.CatTruncatedHeader {
			th = &f.Faults[i]
		}
	}
	if th == nil {
		t.Fatalf("truncated chunk header not detected: %+v", f.Faults)
	}
	if th.ByteStart != 36 || th.ByteEnd != 41 {
		t.Fatalf("truncated header range = [%d,%d), want [36,41)", th.ByteStart, th.ByteEnd)
	}
	if len(media.FatalCategories(f.Faults)) == 0 {
		t.Fatal("truncated header must be quality-fatal")
	}
}

func TestTruncatedFmtPayload(t *testing.T) {
	// RIFF header + fmt header + only 10 fmt payload bytes (need 16).
	short := make([]byte, 12+8+10)
	copy(short[0:4], "RIFF")
	binary.LittleEndian.PutUint32(short[4:8], uint32(4+8+10))
	copy(short[8:12], "WAVE")
	copy(short[12:16], "fmt ")
	binary.LittleEndian.PutUint32(short[16:20], 10)
	f := ParseChunks(short)
	var th *media.Fault
	for i := range f.Faults {
		if f.Faults[i].Category == media.CatTruncatedHeader {
			th = &f.Faults[i]
		}
	}
	if th == nil || th.ByteStart != 20 || th.ByteEnd != 30 {
		t.Fatalf("fmt truncation fault wrong: %+v", f.Faults)
	}
}

func TestMissingChunks(t *testing.T) {
	// RIFF/WAVE header only.
	buf := make([]byte, 12)
	copy(buf[0:4], "RIFF")
	copy(buf[8:12], "WAVE")
	f := ParseChunks(buf)
	if len(media.FatalCategories(f.Faults)) < 2 {
		t.Fatalf("missing fmt/data must yield fatal faults: %+v", f.Faults)
	}
}

func TestOddSizedChunkPadding(t *testing.T) {
	// Layout: RIFF(12) + fmt header(8) + 17-byte payload + pad(1) +
	// data header(8) + 4 payload bytes = 50.
	buf := make([]byte, 50)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(42))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 17) // odd size -> one pad byte
	buf[37] = 0xAB                                 // pad byte
	copy(buf[38:42], "data")
	binary.LittleEndian.PutUint32(buf[42:46], 4)
	f := ParseChunks(buf)
	if len(f.Faults) != 0 {
		t.Fatalf("padded layout must parse cleanly: %+v", f.Faults)
	}
	if f.Data.HeaderStart != 38 || f.Data.DataStart != 46 || f.Data.DataEnd != 50 {
		t.Fatalf("padding offsets wrong: %+v", f.Data)
	}
}
