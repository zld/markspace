package fingerprint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediamerge/internal/model"
	"mediamerge/internal/segment"
)

// seg builds a segment whose sample at index i maps to payloads[i].
func seg(index int, startNs int64, payloads ...[]byte) segment.Segment {
	var samples []model.Sample
	at := model.NsTime(startNs)
	for i, p := range payloads {
		samples = append(samples, model.Sample{
			TrackID: 2, SeqNum: uint32(i + 1), SourceFile: "a.mp4",
			Start: at, Dur: model.NsDuration(1_000_000), Size: uint32(len(p)),
			Index: uint32(i),
		})
		at += model.NsTime(1_000_000)
	}
	return segment.Segment{Index: index, Samples: samples,
		Time: model.Interval{Start: model.NsTime(startNs), End: at}}
}

// payloadIndex maps sample keys to their raw payload bytes.
type payloadIndex map[string][]byte

func (p payloadIndex) get(s model.Sample) ([]byte, bool) {
	b, ok := p[s.Key()]
	return b, ok
}

func indexSegments(segs []segment.Segment, contents [][][]byte) payloadIndex {
	idx := payloadIndex{}
	for si, s := range segs {
		for i, smp := range s.Samples {
			idx[smp.Key()] = contents[si][i]
		}
	}
	return idx
}

func TestBuildFindsExactDuplicate(t *testing.T) {
	content := [][]byte{{1, 2, 3, 4}, {5, 6, 7, 8}}
	s1 := seg(0, 0, content...)
	s2 := seg(1, 500_000_000, content...) // same content, shifted in time
	idx := indexSegments([]segment.Segment{s1, s2}, [][][]byte{content, content})
	rep := Build([]segment.Segment{s1, s2}, []uint32{2}, idx.get, Options{})
	if len(rep.Duplicates) != 1 {
		t.Fatalf("dups=%+v", rep.Duplicates)
	}
	d := rep.Duplicates[0]
	if d.Kept != 0 || d.Dropped != 1 || !d.Verified || d.Bytes != 8 {
		t.Fatalf("dup=%+v", d)
	}
}

func TestBuildDistinguishesDifferentContent(t *testing.T) {
	s1 := seg(0, 0, []byte{1, 1, 1}, []byte{2, 2, 2})
	s2 := seg(1, 10, []byte{9, 9, 9}, []byte{8, 8, 8})
	idx := indexSegments([]segment.Segment{s1, s2}, [][][]byte{
		{{1, 1, 1}, {2, 2, 2}},
		{{9, 9, 9}, {8, 8, 8}},
	})
	rep := Build([]segment.Segment{s1, s2}, nil, idx.get, Options{})
	if len(rep.Duplicates) != 0 {
		t.Fatalf("false duplicate: %+v", rep.Duplicates)
	}
}

func TestBuildCollisionCandidateIsVerified(t *testing.T) {
	// Force every quick hash to the same value; only the per-sample strong
	// digests can tell the contents apart.
	s1 := seg(0, 0, []byte{1, 1, 1}, []byte{2, 2, 2})
	s2 := seg(1, 50, []byte{3, 3, 3}, []byte{4, 4, 4})
	s3 := seg(2, 90, []byte{1, 1, 1}, []byte{2, 2, 2}) // true duplicate of s1
	segs := []segment.Segment{s1, s2, s3}
	idx := indexSegments(segs, [][][]byte{
		{{1, 1, 1}, {2, 2, 2}},
		{{3, 3, 3}, {4, 4, 4}},
		{{1, 1, 1}, {2, 2, 2}},
	})
	opts := Options{WeakHash: func(uint32, []byte, uint32) uint32 { return 7 }}
	rep := Build(segs, []uint32{2}, idx.get, opts)
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Dropped != 2 {
		t.Fatalf("dups=%+v", rep.Duplicates)
	}
	if len(rep.Collisions) != 1 {
		t.Fatalf("collisions=%+v", rep.Collisions)
	}
	c := rep.Collisions[0]
	if c.SegmentA != 0 || c.SegmentB != 1 || c.Quick != rep.Fingerprints[0].Quick {
		t.Fatalf("collision=%+v", c)
	}
}

func TestPartialFingerprintsAreNotDeduped(t *testing.T) {
	content := [][]byte{{1, 2, 3}, {4, 5, 6}}
	s1 := seg(0, 0, content...)
	s2 := seg(1, 100, content...)
	idx := indexSegments([]segment.Segment{s1, s2}, [][][]byte{content, content})
	// Cap is smaller than one sample: content is only partly seen.
	rep := Build([]segment.Segment{s1, s2}, []uint32{2}, idx.get, Options{MaxPartBytes: 2})
	if len(rep.Duplicates) != 0 {
		t.Fatalf("partial content marked duplicate: %+v", rep.Duplicates)
	}
	for _, fp := range rep.Fingerprints {
		if !fp.Partial {
			t.Fatal("truncated fingerprint not marked partial")
		}
	}
}

func TestUnreadablePayloadMarksPartial(t *testing.T) {
	s := seg(0, 0, []byte{1, 2}, []byte{3, 4})
	idx := payloadIndex{s.Samples[0].Key(): {1, 2}} // second sample missing
	rep := Build([]segment.Segment{s}, []uint32{2}, idx.get, Options{})
	if !rep.Fingerprints[0].Partial {
		t.Fatal("missing payload must mark the fingerprint partial")
	}
	if len(rep.Duplicates) != 0 {
		t.Fatal("partial fingerprint deduped")
	}
}

func TestEmptySegmentsAreNotDeduped(t *testing.T) {
	empty := segment.Segment{Index: 0, Time: model.Interval{Start: 0, End: 1_000_000}}
	empty2 := segment.Segment{Index: 1, Time: model.Interval{Start: 1_000_000, End: 2_000_000}}
	rep := Build([]segment.Segment{empty, empty2}, nil,
		func(model.Sample) ([]byte, bool) { return nil, false }, Options{})
	if len(rep.Duplicates) != 0 || len(rep.Collisions) != 0 {
		t.Fatalf("empty segments deduped: %+v", rep)
	}
	if !rep.Fingerprints[0].Empty {
		t.Fatal("empty fingerprint not flagged")
	}
}

func TestWriteFileLocalOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	rep := Build(nil, nil, func(model.Sample) ([]byte, bool) { return nil, false }, Options{})
	if err := rep.WriteFile(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if err := (Report{}).WriteFile(""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestBuildDeterministic(t *testing.T) {
	content := [][]byte{{1, 2}, {3, 4}}
	s1 := seg(0, 0, content...)
	s2 := seg(1, 5, content...)
	idx := indexSegments([]segment.Segment{s1, s2}, [][][]byte{content, content})
	a := Build([]segment.Segment{s1, s2}, []uint32{2}, idx.get, Options{})
	b := Build([]segment.Segment{s1, s2}, []uint32{2}, idx.get, Options{})
	if len(a.Fingerprints) != len(b.Fingerprints) || len(a.Duplicates) != len(b.Duplicates) {
		t.Fatal("fingerprints differ")
	}
	for i := range a.Fingerprints {
		if a.Fingerprints[i].Strong != b.Fingerprints[i].Strong ||
			a.Fingerprints[i].Quick != b.Fingerprints[i].Quick {
			t.Fatalf("fingerprint %d differs", i)
		}
	}
}
