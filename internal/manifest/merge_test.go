package manifest

import "testing"

func TestMergeFingerprintGroupsIsIdempotent(t *testing.T) {
	first := &Manifest{
		TaskKey: "task",
		FingerprintGroups: []FingerprintGroup{{
			Algorithm: "sha256", ContentSHA256: "hash", MaterialID: "mat-b",
			TimelineRefs: []TimelineRef{{SegmentID: "seg-2", TrackID: 1}},
		}},
	}
	second := &Manifest{
		TaskKey: "task",
		FingerprintGroups: []FingerprintGroup{{
			Algorithm: "sha256", ContentSHA256: "hash", MaterialID: "mat-b",
			TimelineRefs: []TimelineRef{
				{SegmentID: "seg-2", TrackID: 1},
				{SegmentID: "seg-4", TrackID: 1},
			},
		}},
	}

	merged := Merge(first, second)
	if len(merged.FingerprintGroups) != 1 {
		t.Fatalf("groups=%d", len(merged.FingerprintGroups))
	}
	g := merged.FingerprintGroups[0]
	if g.Count != 2 || len(g.TimelineRefs) != 2 {
		t.Fatalf("group=%+v", g)
	}

	retry := Merge(merged, second)
	if len(retry.FingerprintGroups) != 1 || retry.FingerprintGroups[0].Count != 2 {
		t.Fatalf("retry merge grew records: %+v", retry.FingerprintGroups)
	}
}

func TestEncodeIsStable(t *testing.T) {
	m := &Manifest{TaskKey: "task", Stages: []string{"scan", "manifest"}}
	_, h1, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	_, h2, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("encode hash not stable")
	}
}
