package track

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func testWAV() []byte {
	// Mono 16-bit, 4 frames.
	data := []byte{0, 0, 1, 0, 2, 0, 3, 0}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(8000))
	_ = binary.Write(&b, binary.LittleEndian, uint32(16000))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

func TestReadWAVNormalizesFrames(t *testing.T) {
	b := testWAV()
	tr, err := ReadWAV("cam", bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Channels != 1 || tr.SampleRate != 8000 || tr.Delivered != 4 || tr.TimelineEnd != 4 {
		t.Fatalf("track=%+v", tr)
	}
	if len(tr.Chunks) != 1 || tr.Chunks[0].SourceOffset != 44 {
		t.Fatalf("chunks=%+v", tr.Chunks)
	}
}

func TestAppendPCMFrameAlignmentAndOverlap(t *testing.T) {
	tr, err := New("a", 48000, 2, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	// One stereo 16-bit frame.
	if err := tr.AppendPCM(0, []byte{1, 0, 2, 0}, 16, 44); err != nil {
		t.Fatal(err)
	}
	if tr.Delivered != 1 {
		t.Fatalf("delivered=%d", tr.Delivered)
	}
	if err := tr.AppendPCM(1, []byte{1, 0}, 16, 48); err == nil {
		t.Fatal("unaligned payload accepted")
	}
	if err := tr.AppendChunk(0, []int32{1, 2}, 44); err == nil {
		t.Fatal("overlap accepted")
	}
}

func TestChannelMappingReordersSources(t *testing.T) {
	m := ChannelMapping{SourceChannels: 2, OutputChannels: 2, Order: []int{1, 0}}
	tr, err := New("a", 48000, 2, 24, &m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tr.Mapping.MapFrame([]int32{10, 20})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 20 || got[1] != 10 {
		t.Fatalf("mapped=%v want [20 10]", got)
	}
}
