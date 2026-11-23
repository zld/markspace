package repair

import (
	"testing"

	"mediapack/internal/clock"
)

func newRepairer() (*TimestampRepairer, *clock.TimelineSet) {
	set := clock.NewTimelineSet()
	return NewTimestampRepairer(set), set
}

func TestRepairSequential(t *testing.T) {
	r, _ := newRepairer()
	d1, err := r.Repair(1, 90000, true, 0, 3000)
	if err != nil || d1.Repaired || d1.Base != 0 {
		t.Fatalf("first fragment: %+v err=%v", d1, err)
	}
	d2, err := r.Repair(1, 90000, true, 3000, 3000)
	if err != nil || d2.Repaired || d2.Base != 3000 {
		t.Fatalf("second fragment: %+v err=%v", d2, err)
	}
}

func TestRepairBackstep(t *testing.T) {
	r, _ := newRepairer()
	if _, err := r.Repair(1, 90000, true, 0, 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Repair(1, 90000, true, 3000, 3000); err != nil {
		t.Fatal(err)
	}
	// tfdt jumps backwards: must be rebuilt from the previous decode end.
	d, err := r.Repair(1, 90000, true, 1000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Repaired || d.Reason != "tfdt-backstep" || d.Base != 6000 {
		t.Errorf("backstep repair: %+v", d)
	}
	// The timeline must stay monotonic afterwards.
	if _, err := r.Repair(1, 90000, true, 9000, 3000); err != nil {
		t.Fatalf("post-repair fragment: %v", err)
	}
}

func TestRepairMissingTfdt(t *testing.T) {
	r, _ := newRepairer()
	if _, err := r.Repair(2, 48000, true, 0, 1024); err != nil {
		t.Fatal(err)
	}
	d, err := r.Repair(2, 48000, false, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Repaired || d.Reason != "missing-tfdt" || d.Base != 1024 {
		t.Errorf("missing tfdt repair: %+v", d)
	}
}

func TestRepairMissingTfdtFirst(t *testing.T) {
	r, _ := newRepairer()
	d, err := r.Repair(2, 48000, false, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != 0 || !d.Repaired {
		t.Errorf("first fragment without tfdt starts at 0: %+v", d)
	}
}

func TestRepairUnrecoverable(t *testing.T) {
	r, _ := newRepairer()
	// No tfdt and no derivable duration: unrecoverable.
	_, err := r.Repair(1, 90000, false, 0, 0)
	if err == nil || !clock.HasCategory(err, clock.CatUnrecoverable) {
		t.Fatalf("want unrecoverable error, got %v", err)
	}
	// Zero duration is unrecoverable even with a tfdt.
	if _, err := r.Repair(1, 90000, true, 0, 0); !clock.HasCategory(err, clock.CatUnrecoverable) {
		t.Fatalf("want unrecoverable error, got %v", err)
	}
}

func TestRepairForwardJumpAccepted(t *testing.T) {
	r, _ := newRepairer()
	if _, err := r.Repair(1, 90000, true, 0, 3000); err != nil {
		t.Fatal(err)
	}
	// A forward jump is not a backstep: the declared tfdt is honored.
	d, err := r.Repair(1, 90000, true, 9000, 3000)
	if err != nil || d.Repaired || d.Base != 9000 {
		t.Errorf("forward jump: %+v err=%v", d, err)
	}
}

func TestSequencerGapDuplicateQuarantine(t *testing.T) {
	s := NewSequencer()
	var h1, h2, h3 [32]byte
	h1[0], h2[0], h3[0] = 1, 2, 3

	d := s.Assign(1, 1, h1)
	if d.Logical != 0 || d.Rebuilt {
		t.Errorf("first: %+v", d)
	}
	// Gap: mfhd jumps 1 -> 4, logical sequence is rebuilt from input order.
	d = s.Assign(1, 4, h2)
	if d.Logical != 1 || !d.Rebuilt {
		t.Errorf("gap: %+v", d)
	}
	// Retransmission of mfhd 1 folds onto its original logical number.
	d = s.Assign(1, 1, h1)
	if !d.Duplicate || d.Logical != 0 {
		t.Errorf("duplicate: %+v", d)
	}
	// Conflicting content under a seen mfhd is quarantined.
	d = s.Assign(1, 4, h3)
	if !d.Quarantined {
		t.Errorf("quarantine: %+v", d)
	}
	// New content continues the logical sequence unaffected.
	d = s.Assign(1, 5, h3)
	if d.Logical != 2 || d.Quarantined {
		t.Errorf("next: %+v", d)
	}
}

func TestSequencerRestore(t *testing.T) {
	s := NewSequencer()
	var h1, h2 [32]byte
	h1[0], h2[0] = 1, 2
	s.Restore(1, 1, 0, h1)
	s.Restore(1, 2, 1, h2)
	d := s.Assign(1, 3, h2) // same hash as mfhd 2's content? no: mfhd 3 is new
	if d.Logical != 2 || d.Rebuilt {
		t.Errorf("resume continues sequence: %+v", d)
	}
	d = s.Assign(1, 1, h1)
	if !d.Duplicate {
		t.Errorf("restored mapping detects retransmit: %+v", d)
	}
}
