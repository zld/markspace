package stream

import "testing"

func TestComputeIntervals(t *testing.T) {
	st := ComputeIntervals([]int64{0, 512, 1024, 1536})
	if st.MinFrames != 512 || st.MaxFrames != 512 || st.AvgFrames != 512 || st.IntervalCount != 3 {
		t.Fatalf("uniform intervals wrong: %+v", st)
	}
	st = ComputeIntervals([]int64{0, 100, 300})
	if st.MinFrames != 100 || st.MaxFrames != 200 || st.SumFrames != 300 || st.AvgFrames != 150 {
		t.Fatalf("varied intervals wrong: %+v", st)
	}
	st = ComputeIntervals(nil)
	if st.IntervalCount != 0 || st.MaxFrames != 0 {
		t.Fatalf("empty boundaries should yield zero stats: %+v", st)
	}
}

func TestFrameCeilDiv(t *testing.T) {
	if FrameCeilDiv(10, 4) != 3 || FrameCeilDiv(8, 4) != 2 || FrameCeilDiv(0, 4) != 0 {
		t.Fatal("ceil division wrong")
	}
}
