package checkpoint

import "testing"

func TestSaveLoadResumePosition(t *testing.T) {
	dir := t.TempDir()
	cp := New()
	cp.State = StateMerging
	cp.CommitPosition("opus-1", 4, 5)
	if err := cp.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.LastCommittedTrack != "opus-1" || loaded.LastRecordPosition != 4 || loaded.CommittedUnits != 5 {
		t.Fatalf("bad checkpoint: %+v", loaded)
	}

	loaded.Pause("cancel-after-units")
	if err := loaded.Save(dir); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != StatePaused || again.CancelReason != "cancel-after-units" {
		t.Fatalf("pause not persisted: %+v", again)
	}
}

func TestLoadMissingReturnsNil(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
