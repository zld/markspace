package pipeline

import (
	"crypto/sha256"
	"testing"

	"rtpregress/internal/audio"
)

func makePoolResult(segNum int, pid uint16, startSample int64, id int) (poolResult, audio.Placement) {
	pcm := []byte{byte(id), 0xBB}
	r := poolResult{
		job: Job{Segment: audio.Segment{ID: id}, PID: pid, SegNumber: segNum},
		result: &audio.Result{
			SegmentID: id, Samples: 2, PCM: pcm, PCMSHA256: sha256.Sum256(pcm),
		},
	}
	pl := audio.Placement{SegmentID: id, PID: pid, StartSample: startSample, Samples: 2}
	return r, pl
}

func TestMergeDeterministicOrder(t *testing.T) {
	// feed in deliberately scrambled order across mixed PIDs and times
	m := NewMerger()
	inputs := []struct {
		segNum      int
		pid         uint16
		startSample int64
		id          int
	}{
		{2, 0x101, 96, 20},
		{0, 0x100, 0, 10},
		{1, 0x100, 48, 11},
		{3, 0x100, 96, 12}, // same start sample as seg 2 but lower PID -> first
	}
	for _, in := range inputs {
		r, pl := makePoolResult(in.segNum, in.pid, in.startSample, in.id)
		m.Add(r, pl)
	}
	out := m.Sorted()
	wantOrder := []int{0, 1, 3, 2} // by sample, then PID, then seg
	for i, w := range wantOrder {
		if out[i].SegNumber != w {
			t.Fatalf("pos %d got seg %d want %d (full %+v)", i, out[i].SegNumber, w, out)
		}
	}
}

func TestMergeHashIndependentOfInsertionOrder(t *testing.T) {
	build := func(order []int) string {
		m := NewMerger()
		base := []struct {
			segNum int
			pid    uint16
			sample int64
			id     int
		}{
			{0, 0x100, 0, 1}, {1, 0x100, 48, 2}, {2, 0x100, 96, 3},
			{3, 0x101, 96, 4},
		}
		for _, idx := range order {
			in := base[idx]
			r, pl := makePoolResult(in.segNum, in.pid, in.sample, in.id)
			m.Add(r, pl)
		}
		return m.ManifestHash()
	}
	h1 := build([]int{0, 1, 2, 3})
	h2 := build([]int{3, 1, 0, 2}) // scrambled arrival, same content
	if h1 != h2 {
		t.Fatalf("manifest hash must not depend on insertion order:\n%s\n%s", h1, h2)
	}
	if h1 == "" {
		t.Fatal("hash must not be empty")
	}
}

func TestMergeHashChangesWhenContentChanges(t *testing.T) {
	m1 := NewMerger()
	r1, pl1 := makePoolResult(0, 0x100, 0, 1)
	m1.Add(r1, pl1)

	m2 := NewMerger()
	// different PCM (id 9) at the same coordinates must change the hash
	r2, pl2 := makePoolResult(0, 0x100, 0, 9)
	m2.Add(r2, pl2)

	if m1.ManifestHash() == m2.ManifestHash() {
		t.Fatal("different PCM content must produce a different manifest hash")
	}
}
