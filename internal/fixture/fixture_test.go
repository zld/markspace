package fixture

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func build(t *testing.T, kind Kind) []byte {
	t.Helper()
	b, err := Build(GeneratePCM(DefaultPlan(kind)))
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return b
}

func boxes(t *testing.T, b []byte) map[string]int {
	t.Helper()
	out := map[string]int{}
	var walk func(from, end, depth int)
	walk = func(from, end, depth int) {
		if depth > 16 {
			return
		}
		for i := from; i+8 <= end; {
			size := int(binary.BigEndian.Uint32(b[i:]))
			typ := string(b[i+4 : i+8])
			out[typ]++
			if size < 8 || i+size > end {
				return
			}
			extra := 0
			switch typ {
			case "stsd":
				extra = 8
			}
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta", "stsd":
				walk(i+8+extra, i+size, depth+1)
			case "mp4a":
				// Sample entry: 8-byte header, 6 reserved, 2 data-ref index,
				// then the versioned audio fields before child boxes.
				walk(i+8+28, i+size, depth+1)
			}
			i += size
		}
	}
	walk(0, len(b), 0)
	return out
}

func TestEveryKindBuildsDeterministically(t *testing.T) {
	for _, kind := range Kinds {
		a := build(t, kind)
		c := build(t, kind)
		if len(a) != len(c) {
			t.Fatalf("%s: build length varies", kind)
		}
		for i := range a {
			if a[i] != c[i] {
				t.Fatalf("%s: build differs at byte %d", kind, i)
			}
		}
	}
}

func TestCleanFixtureStructure(t *testing.T) {
	b := build(t, KindClean)
	seen := boxes(t, b)
	for _, typ := range []string{"ftyp", "moov", "trak", "mdia", "minf", "stbl", "stsd", "stts", "stsc", "stsz", "stco", "mdat", "mp4a", "esds", "elst"} {
		if seen[typ] == 0 {
			t.Fatalf("clean fixture lacks %s", typ)
		}
	}
}

func TestDamageVariantsMatchTheirNames(t *testing.T) {
	if n := boxes(t, build(t, KindNoMoov))["moov"]; n != 0 {
		t.Fatalf("no-moov contains %d moov boxes", n)
	}
	clean := build(t, KindClean)
	if got := len(build(t, KindTruncated)); got >= len(clean) {
		t.Fatalf("truncated is not shorter: %d vs %d", got, len(clean))
	}
	if got := len(build(t, KindGarbled)); got == 0 {
		t.Fatal("garbled fixture is empty")
	}
	if boxes(t, build(t, KindGarbled))["moov"] != 0 {
		t.Fatal("garbled fixture still has moov")
	}
	if n := boxes(t, build(t, KindNoIndex))["stsz"]; n != 1 {
		// The box remains but with zero entries; it must still parse.
		t.Fatalf("no-index stsz count = %d", n)
	}
	cycle := boxes(t, build(t, KindDepCycle))
	if cycle["trak"] != 2 {
		t.Fatalf("dep-cycle tracks = %d, want 2", cycle["trak"])
	}
}

func TestRecordsCarryVerifiableHeaders(t *testing.T) {
	b := build(t, KindClean)
	// Locate mdat and walk its records.
	mdat := -1
	for i := 0; i+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[i:]))
		if string(b[i+4:i+8]) == "mdat" {
			mdat = i
			break
		}
		if size < 8 {
			break
		}
		i += size
	}
	if mdat < 0 {
		t.Fatal("no mdat")
	}
	size := int(binary.BigEndian.Uint32(b[mdat:]))
	pos := mdat + 8
	end := mdat + size
	count := 0
	seq := uint32(0)
	for pos+RecordHeader <= end {
		if binary.BigEndian.Uint16(b[pos:]) != recordMagic {
			t.Fatalf("record %d missing magic at %d", count, pos)
		}
		if b[pos+2] != recordVersion {
			t.Fatalf("record %d version = %d", count, b[pos+2])
		}
		payload := int(binary.BigEndian.Uint32(b[pos+8:]))
		if got := binary.BigEndian.Uint32(b[pos+12:]); got != seq {
			t.Fatalf("record %d seq = %d", count, got)
		}
		frames := int(binary.BigEndian.Uint32(b[pos+16:]))
		ch := int(b[pos+3])
		if payload != frames*ch*2 {
			t.Fatalf("record %d payload %d != %d frames x %d ch", count, payload, frames, ch)
		}
		crc := binary.BigEndian.Uint32(b[pos+20:])
		body := b[pos+RecordHeader : pos+RecordHeader+payload]
		if crc32.ChecksumIEEE(body) != crc {
			t.Fatalf("record %d CRC mismatch", count)
		}
		pos += RecordHeader + payload
		count++
		seq++
	}
	if count == 0 {
		t.Fatal("no records found")
	}
	if pos != end {
		t.Fatalf("records consume %d of %d bytes", pos-mdat-8, end-mdat-8)
	}
}

func TestWriteAndEnsureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(Kinds) {
		t.Fatalf("wrote %d of %d fixtures", len(paths), len(Kinds))
	}
	for _, kind := range Kinds {
		p := paths[kind]
		if filepath.Base(p) != string(kind)+".mp4" {
			t.Fatalf("%s written as %s", kind, p)
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
