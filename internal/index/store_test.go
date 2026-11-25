package index

import (
	"path/filepath"
	"testing"

	"media-audit/internal/source"
)

func TestIndexInvalidationRules(t *testing.T) {
	s := NewStore()
	s.Upsert(Record{TaskKey: "t1", ContentSHA256: "c", StructureSHA256: "s"})

	if s.Invalidate("t1", source.ChangeSet{MetadataChanged: true}) {
		t.Fatal("metadata-only update must not invalidate structural index")
	}
	if _, ok := s.Get("t1"); !ok {
		t.Fatal("index missing after metadata-only change")
	}
	if !s.Invalidate("t1", source.ChangeSet{StructureChanged: true}) {
		t.Fatal("structure change should invalidate index")
	}
	if _, ok := s.Get("t1"); ok {
		t.Fatal("index still present after structure invalidation")
	}

	s.Upsert(Record{TaskKey: "t2", ContentSHA256: "c", StructureSHA256: "s"})
	if !s.Invalidate("t2", source.ChangeSet{ContentReplaced: true}) {
		t.Fatal("content replacement should invalidate index")
	}
}

func TestIndexSaveOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Upsert(Record{TaskKey: "t9", ContentSHA256: "c9", StructureSHA256: "s9"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Get("t9"); !ok {
		t.Fatal("record was not persisted")
	}
}
