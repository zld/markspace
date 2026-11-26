package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
	"example.com/mediavault/internal/model"
)

func sampleEntry(name, content string, blocks int) Entry {
	e := Entry{
		Key:                   model.AssetKey(model.TransportWAV, content),
		Name:                  name,
		Transport:             string(model.TransportWAV),
		Stage:                 string(model.StageArchived),
		ContentDigest:         content,
		SourceContainerDigest: strings.Repeat("1", 64),
		ArchiveDigest:         strings.Repeat("2", 64),
		ContainerDigest:       strings.Repeat("3", 64),
		PolicyVersion:         fingerprint.PolicyVersion,
		SampleRateNum:         8000,
		SampleRateDen:         1,
		Channels:              1,
		BitsPerSample:         16,
		ChunkFrames:           512,
		Frames:                int64(blocks) * 512,
		Blocks:                blocks,
	}
	for i := 0; i < blocks; i++ {
		e.Chunks = append(e.Chunks, Chunk{Index: i, Frames: 512, Length: 1024, Digest: strings.Repeat("a", 64)})
	}
	return e
}

func TestBuildSortsByKeyAndCountsUniquePCM(t *testing.T) {
	a := sampleEntry("a", strings.Repeat("0", 64), 2)
	b := sampleEntry("b", strings.Repeat("f", 64), 3)
	f, err := Build(fingerprint.PolicyVersion, []Entry{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 2 || f.Entries[0].Key >= f.Entries[1].Key {
		t.Fatalf("entries not sorted: %+v", f.Entries)
	}
	if f.UniquePCM != 2 {
		t.Fatalf("unique pcm = %d", f.UniquePCM)
	}
	if !fingerprint.ValidDigest(f.Digest) {
		t.Fatalf("manifest digest %q is not 64 hex characters", f.Digest)
	}
}

func TestBuildIsDeterministicAndOrderIndependent(t *testing.T) {
	a := sampleEntry("a", strings.Repeat("0", 64), 2)
	b := sampleEntry("b", strings.Repeat("f", 64), 1)
	f1, err := Build(fingerprint.PolicyVersion, []Entry{a, b})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := Build(fingerprint.PolicyVersion, []Entry{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if f1.Digest != f2.Digest {
		t.Fatalf("digest depends on input order: %s vs %s", f1.Digest, f2.Digest)
	}
	raw1, _ := f1.Marshal()
	raw2, _ := f2.Marshal()
	if string(raw1) != string(raw2) {
		t.Fatal("serialized manifests differ for the same entry set")
	}
}

func TestDuplicateContentCollapsesUniquePCM(t *testing.T) {
	a := sampleEntry("a", strings.Repeat("0", 64), 1)
	b := sampleEntry("b", strings.Repeat("0", 64), 1)
	b.Key = "wav:renamed:" + b.ContentDigest
	b.Name = "renamed"
	f, err := Build(fingerprint.PolicyVersion, []Entry{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if f.UniquePCM != 1 {
		t.Fatalf("identical PCM must count once, got %d", f.UniquePCM)
	}
}

func TestMarshalRoundTripKeepsDigest(t *testing.T) {
	f, err := Build(fingerprint.PolicyVersion, []Entry{sampleEntry("a", strings.Repeat("0", 64), 2)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest != f.Digest {
		t.Fatalf("digest changed on round trip: %s vs %s", back.Digest, f.Digest)
	}
	again, err := back.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Fatal("serialization is not stable across a round trip")
	}
}

func TestValidateRejectsMalformedManifests(t *testing.T) {
	good, err := Build(fingerprint.PolicyVersion, []Entry{sampleEntry("a", strings.Repeat("0", 64), 1)})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(f *File){
		"version":   func(f *File) { f.Version = "v0" },
		"digest":    func(f *File) { f.Digest = "short" },
		"stage":     func(f *File) { f.Entries[0].Stage = "SCANNED" },
		"blocknum":  func(f *File) { f.Entries[0].Blocks = 9 },
		"chunkdig":  func(f *File) { f.Entries[0].Chunks[0].Digest = "nope" },
		"unordered": func(f *File) { f.Entries = append(f.Entries, f.Entries[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := Build(fingerprint.PolicyVersion, []Entry{sampleEntry("a", strings.Repeat("0", 64), 1)})
			if err != nil {
				t.Fatal(err)
			}
			_ = good
			mutate(f)
			if err := f.Validate(); err == nil {
				t.Fatal("malformed manifest accepted")
			} else if fault.CodeOf(err) != fault.CodeManifest {
				t.Fatalf("code = %q", fault.CodeOf(err))
			}
		})
	}
}

func TestUnmarshalRejectsUnknownJSON(t *testing.T) {
	if _, err := Unmarshal([]byte("{")); err == nil {
		t.Fatal("broken JSON accepted")
	}
	var f File
	raw := []byte(`{"version":"mediavault-manifest-v1","entries":[]}`)
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if fault.CodeOf(f.Validate()) != fault.CodeManifest {
		t.Fatal("a manifest without a digest must be rejected")
	}
}

func TestVerifySourcesChecksBlockOrder(t *testing.T) {
	e := sampleEntry("a", strings.Repeat("0", 64), 2)
	if err := VerifySources([]Entry{e}); err != nil {
		t.Fatalf("valid entry: %v", err)
	}
	bad := sampleEntry("a", strings.Repeat("0", 64), 2)
	bad.Chunks[1].Index = 5
	if err := VerifySources([]Entry{bad}); fault.CodeOf(err) != fault.CodeManifest {
		t.Fatalf("out-of-order block: %v", err)
	}
}
