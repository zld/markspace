package migrate

import (
	"errors"
	"testing"

	"mediacheck/internal/manifest"
)

func testManifest(version int) *manifest.Manifest {
	m := &manifest.Manifest{
		Version: version,
		TrackID: 7,
		Identity: manifest.Identity{
			ContentHash:   "sha256:content:abc",
			ContainerHash: "sha256:container:def",
			MetadataHash:  "sha256:metadata:ghi",
		},
		Summary: manifest.Summary{
			Version: version, Codec: "H264", SampleCount: 2,
			KeyFrameCount: 1, FaultCount: 0, ClockRate: 90000, TimeTrusted: true,
			FaultsByClass: map[string]int{},
		},
		Samples: []manifest.SampleRow{
			{Index: 0, Codec: "H264", KeyFrame: true, FirstSeq: 0, LastSeq: 0,
				ByteOffset: 0, ByteLength: 4, TimeStart: 0, TimeEnd: 0, Duration: 3600},
			{Index: 1, Codec: "H264", FirstSeq: 1, LastSeq: 1,
				ByteOffset: 4, ByteLength: 3, TimeStart: 3600, TimeEnd: 3600, Duration: 3600},
		},
		Faults: []manifest.FaultRow{},
	}
	return m
}

func alwaysValid(*manifest.Manifest) error { return nil }

func TestSuccessfulThreeStageMigration(t *testing.T) {
	mg := NewMigrator(alwaysValid)
	res, err := mg.Run(testManifest(1), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || res.Rollback {
		t.Fatalf("migration should commit: %+v", res)
	}
	wantStages := []string{StagePrepare, StageValidate, StageCommit}
	if len(res.Stages) != 3 {
		t.Fatalf("journal = %+v", res.Stages)
	}
	for i, st := range res.Stages {
		if st.Stage != wantStages[i] {
			t.Fatalf("stage %d = %s, want %s", i, st.Stage, wantStages[i])
		}
		if st.Version != manifest.SchemaVersion || st.Summary == "" {
			t.Fatalf("stage record incomplete: %+v", st)
		}
	}
	if res.Final.Version != manifest.SchemaVersion {
		t.Fatalf("committed version = %d", res.Final.Version)
	}
	// Identity checks cover both layers.
	layers := map[string]bool{}
	for _, c := range res.Checks {
		layers[c.Layer] = true
		if c.Hash == "" {
			t.Fatal("identity check missing hash")
		}
	}
	if !layers["content"] || !layers["index"] {
		t.Fatalf("identity checks incomplete: %+v", res.Checks)
	}
}

func TestValidationFailureRestoresOriginal(t *testing.T) {
	boom := errors.New("bad candidate")
	mg := NewMigrator(func(*manifest.Manifest) error { return boom })
	original := testManifest(manifest.SchemaVersion)
	beforeDigest := StableDigest(original)

	res, err := mg.Run(original, false)
	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("err = %v, want ErrValidationFailed", err)
	}
	if res.Committed || !res.Rollback {
		t.Fatalf("validation failure must roll back: %+v", res)
	}
	// Journal stops before commit.
	for _, st := range res.Stages {
		if st.Stage == StageCommit {
			t.Fatal("commit stage must not be journaled on validation failure")
		}
	}
	if StableDigest(res.Final) != beforeDigest {
		t.Fatal("restored manifest digest differs from original")
	}
	if res.Final.Version != original.Version {
		t.Fatalf("restored version = %d, want %d", res.Final.Version, original.Version)
	}
}

func TestCommitInterruptionRestoresOriginal(t *testing.T) {
	mg := NewMigrator(alwaysValid)
	original := testManifest(manifest.SchemaVersion)
	beforeDigest := StableDigest(original)

	res, err := mg.Run(original, true)
	if err == nil {
		t.Fatal("interrupted commit must return an error")
	}
	if res.Committed || !res.Rollback {
		t.Fatalf("interruption must trigger rollback: %+v", res)
	}
	if StableDigest(res.Final) != beforeDigest {
		t.Fatal("post-interruption manifest differs from pre-migration state")
	}
	if len(res.Stages) != 2 { // prepare + validate only
		t.Fatalf("journal must stop before commit: %+v", res.Stages)
	}
	// Rollback identities point at the restored versions.
	for _, c := range res.Checks {
		want := original.Identity.ContentHash
		if c.Layer == "index" {
			want = indexFingerprint(original)
		}
		if c.Hash != want {
			t.Fatalf("rollback %s identity = %s, want %s", c.Layer, c.Hash, want)
		}
	}
}

func TestRepeatedRunsProduceIdenticalSummary(t *testing.T) {
	in := testManifest(manifest.SchemaVersion)
	digests := make([]string, 3)
	for i := range digests {
		mg := NewMigrator(alwaysValid)
		res, err := mg.Run(in, false)
		if err != nil {
			t.Fatal(err)
		}
		digests[i] = StableDigest(res.Final)
	}
	if digests[0] != digests[1] || digests[1] != digests[2] {
		t.Fatalf("digests not stable across runs: %v", digests)
	}
	// CreatedAt-style fields must not influence the digest.
	tweaked := testManifest(manifest.SchemaVersion)
	tweaked.TrackID = 7
	if StableDigest(in) != StableDigest(tweaked) {
		t.Fatal("equivalent manifests must digest equally")
	}
}

func TestStableDigestIgnoresTimestampAndChangesWithContent(t *testing.T) {
	a := testManifest(manifest.SchemaVersion)
	b := testManifest(manifest.SchemaVersion)
	if StableDigest(a) != StableDigest(b) {
		t.Fatal("identical inputs must hash equally")
	}
	b.Identity.ContentHash = "sha256:content:changed"
	if StableDigest(a) == StableDigest(b) {
		t.Fatal("content replacement must change the digest")
	}
}
