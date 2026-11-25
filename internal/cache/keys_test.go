package cache

import (
	"testing"

	"media-audit/internal/source"
)

func testFP(version string) source.Fingerprint {
	return source.Fingerprint{
		Path: "/tmp/a.mxf", ContentSHA256: "c1", StructureSHA256: "s1", MetadataVersion: version,
	}
}

func TestMetadataOnlyInvalidatesMetadataAndManifest(t *testing.T) {
	s := NewStore()
	k := KeysFor("task1", testFP("m1"))
	for _, key := range []string{k.Parsed, k.Metadata, k.Manifest} {
		s.Put(key, true)
	}
	removed := s.InvalidateForChange(k, source.ChangeSet{MetadataChanged: true})
	if len(removed) != 2 {
		t.Fatalf("removed = %v", removed)
	}
	if _, ok := s.Get(k.Parsed); !ok {
		t.Fatal("parsed cache should survive metadata-only update")
	}
	if _, ok := s.Get(k.Metadata); ok {
		t.Fatal("metadata cache should be invalid")
	}
}

func TestContentReplacementInvalidatesAll(t *testing.T) {
	s := NewStore()
	k := KeysFor("task1", testFP("m1"))
	for _, key := range []string{k.Parsed, k.Metadata, k.Recovery, k.Manifest} {
		s.Put(key, true)
	}
	removed := s.InvalidateForChange(k, source.ChangeSet{ContentReplaced: true, StructureChanged: true})
	if len(removed) != 4 {
		t.Fatalf("removed = %v", removed)
	}
	if s.Len() != 0 {
		t.Fatalf("cache not empty: %d", s.Len())
	}
}
