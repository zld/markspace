package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"avcheck/internal/media"
)

func TestMP4BuilderMatchesBatch(t *testing.T) {
	tracks, _ := Generate(testConfig())
	frames := tracks[0]
	dir := t.TempDir()
	batch := filepath.Join(dir, "batch.mp4")
	if err := WriteMP4(batch, 90000, frames); err != nil {
		t.Fatal(err)
	}
	streamed := filepath.Join(dir, "stream.mp4")
	b, err := NewMP4Builder(streamed)
	if err != nil {
		t.Fatal(err)
	}
	for i := range frames {
		if err := b.Add(&frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(90000); err != nil {
		t.Fatal(err)
	}
	bs, err := ReadSampleTable(batch)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := ReadSampleTable(streamed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bs, ss) {
		t.Fatalf("sample tables differ:\n batch=%+v\n stream=%+v", bs, ss)
	}
	if _, err := os.Stat(streamed + ".mdat.tmp"); !os.IsNotExist(err) {
		t.Fatal("temp payload file must be removed")
	}
}

func TestMP4BuilderRejectsRegression(t *testing.T) {
	dir := t.TempDir()
	b, err := NewMP4Builder(filepath.Join(dir, "r.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(seq uint64, ticks int64, key bool) *media.Frame {
		f := &media.Frame{StreamID: 3, Seq: seq, Ticks: ticks, Key: key,
			Meta: media.Meta{Version: media.MetaVersion, Length: 4}, Payload: []byte{1, 2, 3, 4}}
		f.Sum = f.ComputeSum()
		return f
	}
	if err := b.Add(mk(1, 100, true)); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(mk(2, 100, false)); err == nil {
		t.Fatal("non-increasing ticks must be rejected")
	}
	if err := b.Add(mk(2, 200, false)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(90000); err != nil {
		t.Fatal(err)
	}
	st, err := ReadSampleTable(filepath.Join(dir, "r.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Samples) != 2 || !st.Samples[0].Key || st.Samples[0].Duration != 100 {
		t.Fatalf("samples: %+v", st.Samples)
	}
}
