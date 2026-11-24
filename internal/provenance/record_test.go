package provenance_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
	"mediad/internal/provenance"
)

func TestWriteAndReadBack(t *testing.T) {
	dir := t.TempDir()
	b := provenance.NewBuilder("case-1",
		map[string]string{"mp4": "mp4-v1"},
		map[string]string{"target": "2s"})
	b.AddChunk(provenance.ChunkRecord{
		ChunkID: "v-c0000", Hash: "deadbeef", Representation: "v",
		Sources: []provenance.SourceRef{
			{Manifest: "m.mpd", Representation: "v", Entry: 0, File: "v/1.m4s"},
		},
	})
	b.AddError(*model.NewError(model.KindCorruptBox,
		model.SourceLoc{Manifest: "m.mpd", Entry: 3, Frame: 2}, "truncated"))
	path, err := b.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "case-1.json" {
		t.Fatalf("path = %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec provenance.RunRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.CaseID != "case-1" || rec.ParserVersions["mp4"] != "mp4-v1" {
		t.Fatalf("record header wrong: %+v", rec)
	}
	if len(rec.Chunks) != 1 || rec.Chunks[0].Sources[0].Entry != 0 {
		t.Fatalf("chunk lineage lost: %+v", rec.Chunks)
	}
	if len(rec.Errors) != 1 || rec.Errors[0].Kind != model.KindCorruptBox {
		t.Fatalf("sourced error lost: %+v", rec.Errors)
	}
}

func TestChunksSortedDeterministically(t *testing.T) {
	dir := t.TempDir()
	b := provenance.NewBuilder("c", nil, nil)
	for _, id := range []string{"z", "a", "m"} {
		b.AddChunk(provenance.ChunkRecord{ChunkID: id, Representation: "r"})
	}
	if _, err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "provenance", "c.json"))
	var rec provenance.RunRecord
	_ = json.Unmarshal(raw, &rec)
	ids := []string{rec.Chunks[0].ChunkID, rec.Chunks[1].ChunkID, rec.Chunks[2].ChunkID}
	if ids[0] != "a" || ids[1] != "m" || ids[2] != "z" {
		t.Fatalf("not sorted: %v", ids)
	}
}

func TestSilenceRecordCarriesParameters(t *testing.T) {
	b := provenance.NewBuilder("s", nil, nil)
	b.AddSilence(provenance.SilenceRecord{
		Representation: "a",
		Intervals: []map[string]any{
			{"start_ns": 0, "end_ns": 1000},
		},
	})
	rec := b.Record()
	if len(rec.Silence) != 1 || rec.Silence[0].Representation != "a" {
		t.Fatalf("silence record wrong: %+v", rec.Silence)
	}
}
