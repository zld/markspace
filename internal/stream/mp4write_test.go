package stream

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// minimalMP4 writes a tiny single-track MP4 by hand. The stream package cannot
// import internal/fixtures (fixtures imports stream for capture writing), so
// structural tests build their own bytes.
func minimalMP4(t *testing.T) []byte {
	t.Helper()
	type smp struct{ size, delta uint32 }
	samples := []smp{{100, 3000}, {120, 3000}, {140, 3000}, {160, 3000}, {180, 3000}}
	var stts, stsz, stss []byte
	put := func(dst *[]byte, v uint32) { *dst = binary.BigEndian.AppendUint32(*dst, v) }
	put(&stts, 1)
	put(&stts, uint32(len(samples)))
	put(&stts, 3000)
	put(&stsz, 0)
	put(&stsz, uint32(len(samples)))
	syncCount := uint32(0)
	for i, s := range samples {
		put(&stsz, s.size)
		if i%2 == 0 {
			syncCount++
			put(&stss, uint32(i+1))
		}
	}
	box := func(typ string, payload []byte) []byte {
		out := make([]byte, 0, 8+len(payload))
		out = binary.BigEndian.AppendUint32(out, uint32(8+len(payload)))
		out = append(out, typ...)
		return append(out, payload...)
	}
	full := func(typ string, v uint8, payload []byte) []byte {
		return box(typ, append([]byte{v, 0, 0, 0}, payload...))
	}
	sttsBox := full("stts", 0, stts)
	stszBox := full("stsz", 0, stsz)
	stssBox := full("stss", 0, append(binary.BigEndian.AppendUint32(nil, syncCount), stss...))
	stsc := full("stsc", 0, func() []byte {
		b := binary.BigEndian.AppendUint32(nil, 1)
		b = binary.BigEndian.AppendUint32(b, 1)
		b = binary.BigEndian.AppendUint32(b, uint32(len(samples)))
		b = binary.BigEndian.AppendUint32(b, 1)
		return b
	}())
	stco := full("stco", 0, func() []byte {
		b := binary.BigEndian.AppendUint32(nil, 1)
		b = binary.BigEndian.AppendUint32(b, 0) // patched below
		return b
	}())
	mdhd := full("mdhd", 0, func() []byte {
		b := make([]byte, 0, 20)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 90000)
		b = binary.BigEndian.AppendUint32(b, 15000)
		b = binary.BigEndian.AppendUint16(b, 0x55c4)
		b = binary.BigEndian.AppendUint16(b, 0)
		return b
	}())
	hdlr := full("hdlr", 0, func() []byte {
		b := binary.BigEndian.AppendUint32(nil, 0)
		b = append(b, "vide"...)
		b = append(b, make([]byte, 12)...)
		return append(b, 0)
	}())
	tkhd := full("tkhd", 0, func() []byte {
		b := make([]byte, 0, 80)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 1)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 15000)
		return append(b, make([]byte, 60)...)
	}())
	vmhd := full("vmhd", 0, make([]byte, 8))
	dref := full("dref", 0, func() []byte { return binary.BigEndian.AppendUint32(nil, 0) }())
	dinf := box("dinf", dref)
	stsd := full("stsd", 0, binary.BigEndian.AppendUint32(nil, 0))
	stbl := box("stbl", concatBytes(stsd, sttsBox, stssBox, stsc, stszBox, stco))
	minf := box("minf", concatBytes(vmhd, dinf, stbl))
	mdia := box("mdia", concatBytes(mdhd, hdlr, minf))
	trak := box("trak", concatBytes(tkhd, mdia))
	mvhd := full("mvhd", 0, func() []byte {
		b := make([]byte, 0)
		for i := 0; i < 4; i++ {
			b = binary.BigEndian.AppendUint32(b, 0)
		}
		return append(b, make([]byte, 80)...)
	}())
	moov := box("moov", concatBytes(mvhd, trak))
	ftyp := box("ftyp", append([]byte("isom\x00\x00\x02\x00"), []byte("isommp41")...))
	mdatHeader := len(ftyp) + len(moov) + 8
	// Patch the chunk offset now that the layout is known.
	off := binary.BigEndian.AppendUint32(nil, uint32(mdatHeader))
	stco = full("stco", 0, func() []byte {
		b := binary.BigEndian.AppendUint32(nil, 1)
		return append(b, off...)
	}())
	stbl = box("stbl", concatBytes(stsd, sttsBox, stssBox, stsc, stszBox, stco))
	minf = box("minf", concatBytes(vmhd, dinf, stbl))
	mdia = box("mdia", concatBytes(mdhd, hdlr, minf))
	trak = box("trak", concatBytes(tkhd, mdia))
	moov = box("moov", concatBytes(mvhd, trak))
	mdat := box("mdat", make([]byte, 700))
	out := concatBytes(ftyp, moov, mdat)
	return out
}

func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestMinimalMP4BuildsAndAnalyzes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "min.mp4")
	writeTestFile(t, p, minimalMP4(t))
	a, err := AnalyzeMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Video == nil {
		t.Fatalf("no video track: %+v", a.Faults)
	}
	if len(a.Video.Samples) != 5 {
		t.Fatalf("samples = %d", len(a.Video.Samples))
	}
	if a.Video.Samples[0].Offset == 0 {
		t.Fatal("chunk offset not resolved")
	}
}
