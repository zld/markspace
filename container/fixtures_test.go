package container

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func box(boxType string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:8], boxType)
	return append(out, body...)
}

// fullBox writes a full box with a version and 24-bit flags.
func fullBox(boxType string, version uint8, flags uint32, body []byte) []byte {
	header := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(boxType, append(header, body...))
}

func u32(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

func u64(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// ftyp is a minimal file-type box.
func ftyp() []byte {
	return box("ftyp", cat([]byte("isom"), u32(0x200), []byte("isom"), []byte("iso2"), []byte("mp41")))
}

type trackSpec struct {
	handle     string // "vide", "soun", "subt"
	trackID    uint32
	timescale  uint32
	sampleDur  uint32
	sampleSize uint32
	sampleCnt  uint32
	chunkSize  uint32 // samples per chunk
	// chunkOffsetShift is added to every chunk offset, to simulate an index
	// that points past the end of the file.
	chunkOffsetShift int64
	withSync         bool
}

// buildMoov assembles a moov box describing the given tracks. The returned
// chunk offsets are relative to the first mdat payload; callers must add the
// real payload start.
func buildMoov(specs []trackSpec, mdatDataStart int64) []byte {
	var traks [][]byte
	for _, spec := range specs {
		traks = append(traks, buildTrak(spec, mdatDataStart))
	}
	return box("moov", cat(
		buildMvhd(specs),
		cat(traks...),
	))
}

func buildMvhd(specs []trackSpec) []byte {
	var duration uint64
	var timescale uint32 = 1000
	for _, spec := range specs {
		if d := uint64(spec.sampleDur) * uint64(spec.sampleCnt) * 1000 / uint64(spec.timescale); d > duration {
			duration = d
		}
	}
	body := cat(
		u32(0), u32(0), // creation, modification
		u32(timescale), u32(uint32(duration)),
		u32(0x00010000),    // rate
		[]byte{0x01, 0x00}, // volume
		make([]byte, 10),   // reserved
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		make([]byte, 24), // pre_defined
		u32(uint32(len(specs))+1),
	)
	return fullBox("mvhd", 0, 0, body)
}

func buildTrak(spec trackSpec, mdatDataStart int64) []byte {
	duration := uint64(spec.sampleDur) * uint64(spec.sampleCnt)
	tkhd := fullBox("tkhd", 0, 7, cat(
		u32(0), u32(0),
		u32(spec.trackID), u32(0),
		u32(uint32(duration)),
		make([]byte, 8),
		make([]byte, 8), // layer, alternate group, volume, reserved
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(640<<16), u32(360<<16),
	))
	mdhd := fullBox("mdhd", 0, 0, cat(
		u32(0), u32(0),
		u32(spec.timescale), u32(uint32(duration)),
		[]byte{0x55, 0xC4, 0}, // "und" packed language
		u32(0),
	))
	hdlr := fullBox("hdlr", 0, 0, cat(
		u32(0), []byte(spec.handle), make([]byte, 12), []byte("mediarc\x00"),
	))
	stts := fullBox("stts", 0, 0, cat(
		u32(1), u32(spec.sampleCnt), u32(spec.sampleDur),
	))
	stsz := fullBox("stsz", 0, 0, cat(
		u32(spec.sampleSize), u32(spec.sampleCnt),
	))
	chunks := (spec.sampleCnt + spec.chunkSize - 1) / spec.chunkSize
	var offsets [][]byte
	for i := uint32(0); i < chunks; i++ {
		offset := mdatDataStart + int64(i)*int64(spec.chunkSize)*int64(spec.sampleSize) + spec.chunkOffsetShift
		offsets = append(offsets, u32(uint32(offset)))
	}
	stco := fullBox("stco", 0, 0, cat(u32(chunks), cat(offsets...)))
	stsc := fullBox("stsc", 0, 0, cat(u32(1), u32(1), u32(spec.chunkSize), u32(1)))
	var stblParts [][]byte
	stblParts = append(stblParts, fullBox("stsd", 0, 0, u32(0)), stts, stsz, stsc, stco)
	if spec.withSync {
		var numbers []byte
		for i := uint32(1); i <= spec.sampleCnt; i += 4 {
			numbers = append(numbers, u32(i)...)
		}
		stblParts = append(stblParts, fullBox("stss", 0, 0, cat(u32(uint32(len(numbers)/4)), numbers)))
	}
	stbl := box("stbl", cat(stblParts...))
	minf := box("minf", cat(box("vmhd", cat(u32(1), make([]byte, 8))), stbl))
	mdia := box("mdia", cat(mdhd, hdlr, minf))
	return box("trak", cat(tkhd, mdia))
}

// fixture is a generated MP4 on disk plus the knobs used to make it.
type fixture struct {
	path     string
	size     int64
	traks    []trackSpec
	mdatData int64
}

// writeFixture assembles an MP4 with one video and one audio track and writes
// it to dir. chunkOffsetShift widens every chunk offset.
func writeFixture(t *testing.T, dir string, chunkOffsetShift int64) fixture {
	t.Helper()
	specs := []trackSpec{
		{handle: "vide", trackID: 1, timescale: 90000, sampleDur: 3000, sampleSize: 512, sampleCnt: 20, chunkSize: 5, chunkOffsetShift: chunkOffsetShift, withSync: true},
		{handle: "soun", trackID: 2, timescale: 48000, sampleDur: 1024, sampleSize: 256, sampleCnt: 40, chunkSize: 10, chunkOffsetShift: chunkOffsetShift},
	}
	// ftyp + moov + mdat header; computed after moov is built, which depends
	// on the mdat payload start. Because moov's size does not depend on the
	// offset values, one pre-pass fixes the layout exactly.
	mdatPayloadLen := int64(0)
	for _, spec := range specs {
		mdatPayloadLen += int64(spec.sampleSize) * int64(spec.sampleCnt)
	}
	moovProbe := buildMoov(specs, 0)
	mdatDataStart := int64(len(ftyp())) + int64(len(moovProbe)) + 8
	moov := buildMoov(specs, mdatDataStart)
	if len(moov) != len(moovProbe) {
		t.Fatalf("moov size changed after offset substitution: %d vs %d", len(moov), len(moovProbe))
	}
	mdatPayload := make([]byte, mdatPayloadLen)
	for i := range mdatPayload {
		mdatPayload[i] = byte(i % 251)
	}
	mdatHeader := make([]byte, 8)
	binary.BigEndian.PutUint32(mdatHeader, uint32(8+len(mdatPayload)))
	copy(mdatHeader[4:], "mdat")
	data := cat(ftyp(), moov, mdatHeader, mdatPayload)
	path := filepath.Join(dir, fmt.Sprintf("fixture-%d.mp4", chunkOffsetShift))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return fixture{path: path, size: int64(len(data)), traks: specs, mdatData: mdatDataStart}
}
