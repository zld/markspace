package index_test

import (
	"testing"

	"assetcheck/internal/errmodel"
	"assetcheck/internal/fixture"
	"assetcheck/internal/index"
	"assetcheck/internal/stream"
	"assetcheck/internal/txn"
	"assetcheck/internal/wav"
)

func goodReport(t *testing.T) (*wav.Wav, *stream.Report) {
	t.Helper()
	data := fixture.NamedFixtures()["good.wav"]
	w, err := wav.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := stream.Scan(w, data, stream.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	return w, rep
}

func TestBuildAndRoundTrip(t *testing.T) {
	w, rep := goodReport(t)
	idx, err := index.Build(rep, wav.ContentFingerprint(w), wav.MetadataFingerprint(nil, w))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Keys) != 188 {
		t.Fatalf("keys=%d", len(idx.Keys))
	}
	// First key is frame 0 / PTS 0; a mid key has integer PTS.
	if idx.Keys[0].Frame != 0 || idx.Keys[0].Nanos != 0 || idx.Keys[1].Nanos == 0 {
		t.Fatalf("key PTS wrong: %+v %+v", idx.Keys[0], idx.Keys[1])
	}
	k, ok := idx.AtOrBefore(513)
	if !ok || k.Frame != 512 {
		t.Fatalf("AtOrBefore(513)=%+v ok=%v", k, ok)
	}
	b, err := idx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := index.UnmarshalIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest != idx.Digest {
		t.Fatal("digest changed across JSON round trip")
	}
}

func TestDuplicateKeyRejected(t *testing.T) {
	idx := &index.Index{
		Schema: index.SchemaVersion, SampleRate: 48000,
		Keys: []index.Key{{Frame: 0}, {Frame: 512}, {Frame: 512}},
	}
	err := idx.Validate()
	if e, ok := errmodel.AsError(err); !ok || e.Code != errmodel.ErrDuplicateKey {
		t.Fatalf("want E3001, got %v", err)
	}
}

func TestStoreVisibilityAndPersistence(t *testing.T) {
	dir := t.TempDir()
	w, rep := goodReport(t)
	contentFP := wav.ContentFingerprint(w)
	idx, err := index.Build(rep, contentFP, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := txn.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	txid, seq, err := mgr.Begin()
	if err != nil {
		t.Fatal(err)
	}
	a := store.NewArtifact(txid, contentFP, seq)
	if err := store.WriteStaging(&a, idx); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordPrepared(a); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Commit(a, store); err != nil {
		t.Fatal(err)
	}
	visible, e, err := store.Visible(contentFP)
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 || visible.Digest != idx.Digest {
		t.Fatalf("visible entry wrong: %+v", e)
	}
}
