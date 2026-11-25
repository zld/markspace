package ingest

import (
	"bytes"
	"encoding/binary"
	"testing"

	"rtpreplay/media"
	"rtpreplay/meta"
)

// KLV builder for the sample MXF subset.

func berLength(n uint64) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var body []byte
	for v := n; v > 0; v >>= 8 {
		body = append([]byte{byte(v)}, body...)
	}
	return append([]byte{0x80 | byte(len(body))}, body...)
}

func klv(key [16]byte, val []byte) []byte {
	var out bytes.Buffer
	out.Write(key[:])
	out.Write(berLength(uint64(len(val))))
	out.Write(val)
	return out.Bytes()
}

func cdciValue(w, h uint32, cp, tc, mc uint16, full byte) []byte {
	var v bytes.Buffer
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], w)
	v.Write(b4[:])
	binary.BigEndian.PutUint32(b4[:], h)
	v.Write(b4[:])
	var b2 [2]byte
	for _, x := range []uint16{cp, tc, mc} {
		binary.BigEndian.PutUint16(b2[:], x)
		v.Write(b2[:])
	}
	v.WriteByte(full)
	return v.Bytes()
}

func sampleMXF() []byte {
	var out bytes.Buffer
	out.Write(klv(keyPartitionPack, []byte{0x01, 0x02, 0x03, 0x04}))
	out.Write(klv(keyPreface, []byte("preface-body")))

	var sp bytes.Buffer
	sp.Write(klv(keyCDCI, cdciValue(1280, 720, 9, 16, 9, 1)))
	var rot [4]byte
	binary.BigEndian.PutUint32(rot[:], 180)
	sp.Write(klv(keyRotation, rot[:]))
	crop := make([]byte, 24)
	for i := 0; i < 6; i++ {
		binary.BigEndian.PutUint32(crop[i*4:], uint32(10+i))
	}
	sp.Write(klv(keyCrop, crop))
	out.Write(klv(keySourcePackage, sp.Bytes()))
	return out.Bytes()
}

func TestReadMXFNormal(t *testing.T) {
	data := sampleMXF()
	md, err := ReadMXF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(md.Tracks))
	}
	tr := md.Tracks[0]
	if tr.Width != 1280 || tr.Height != 720 {
		t.Fatalf("dims = %dx%d", tr.Width, tr.Height)
	}
	fields := map[string]bool{}
	for _, c := range tr.Candidates {
		fields[c.Field] = true
		if c.Priority != meta.PriorityMXF {
			t.Fatalf("wrong priority on %s", c.Source)
		}
		if c.Field == meta.FieldColor && c.Color.ColourPrimaries != 9 {
			t.Fatalf("color wrong: %+v", c.Color)
		}
		if c.Field == meta.FieldRotation && c.Rotation.Degrees != 180 {
			t.Fatalf("rotation wrong: %+v", c.Rotation)
		}
		if c.Field == meta.FieldCrop && c.Crop.Width != 14 {
			t.Fatalf("crop wrong: %+v", c.Crop)
		}
	}
	for _, f := range []string{meta.FieldRotation, meta.FieldCrop, meta.FieldColor} {
		if !fields[f] {
			t.Fatalf("missing field %s", f)
		}
	}
}

func TestReadMXFBerLongForm(t *testing.T) {
	// A value whose length needs the long-form BER (>= 128 bytes).
	val := bytes.Repeat([]byte{0xAB}, 200)
	data := append(sampleMXF(), klv(keyPartitionPack, val)...)
	md, err := ReadMXF(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Tracks) != 1 {
		t.Fatal("long-form BER item must be parsed without loss")
	}
}

func TestReadMXFTruncatedKey(t *testing.T) {
	data := sampleMXF()
	corrupt := data[:len(data)-10] // chop inside the last item
	_, err := ReadMXF(bytes.NewReader(corrupt), int64(len(corrupt)))
	le, ok := media.AsLocatable(err)
	if !ok {
		t.Fatalf("want locatable error, got %v", err)
	}
	if le.Code != "TRUNCATED_KLV_KEY" && le.Code != "TRUNCATED_KLV_VALUE" && le.Code != "LENGTH_OUT_OF_BOUNDS" {
		t.Fatalf("unexpected code %s", le.Code)
	}
}

func TestReadMXFLengthOutOfBounds(t *testing.T) {
	// Honest 16-byte key, then a BER length claiming far more than remains.
	var out bytes.Buffer
	out.Write(keyPartitionPack[:])
	out.Write([]byte{0x80 | 0x04, 0x00, 0x10, 0x00, 0x00}) // 1,048,576 bytes
	out.Write([]byte{1, 2, 3})
	_, err := ReadMXF(bytes.NewReader(out.Bytes()), int64(out.Len()))
	le, ok := media.AsLocatable(err)
	if !ok || le.Code != "LENGTH_OUT_OF_BOUNDS" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMXFMissingPartition(t *testing.T) {
	var out bytes.Buffer
	out.Write(klv(keyPreface, []byte("x")))
	_, err := ReadMXF(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if le, ok := media.AsLocatable(err); !ok || le.Code != "NO_PARTITION_PACK" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMXFMissingPreface(t *testing.T) {
	var out bytes.Buffer
	out.Write(klv(keyPartitionPack, []byte("x")))
	_, err := ReadMXF(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if le, ok := media.AsLocatable(err); !ok || le.Code != "NO_PREFACE" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMXFBadRotation(t *testing.T) {
	var sp bytes.Buffer
	sp.Write(klv(keyCDCI, cdciValue(64, 64, 9, 16, 9, 0)))
	var rot [4]byte
	binary.BigEndian.PutUint32(rot[:], 45) // not a quarter turn
	sp.Write(klv(keyRotation, rot[:]))
	var out bytes.Buffer
	out.Write(klv(keyPartitionPack, []byte("p")))
	out.Write(klv(keyPreface, []byte("p")))
	out.Write(klv(keySourcePackage, sp.Bytes()))
	_, err := ReadMXF(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if le, ok := media.AsLocatable(err); !ok || le.Code != "BAD_ROTATION" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMXFSizeCap(t *testing.T) {
	_, err := ReadMXF(bytes.NewReader(nil), media.MaxInputBytes+1)
	if le, ok := media.AsLocatable(err); !ok || le.Code != "INPUT_TOO_LARGE" {
		t.Fatalf("got %v", err)
	}
}
