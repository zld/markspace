package fixture

import "testing"

func TestBuildRTPRejectsBadPlan(t *testing.T) {
	for _, plan := range []RTPPlan{
		{PacketCount: 0},
		{PacketCount: 5, PayloadLen: 0},
		{PacketCount: 5, PayloadLen: 2048},
	} {
		if _, err := BuildRTP(plan); err == nil {
			t.Fatalf("plan %+v should be rejected", plan)
		}
	}
}

func TestBuildRTPCountsSurvivors(t *testing.T) {
	sess, err := BuildRTP(RTPPlan{PacketCount: 10, PayloadLen: 4, StartSeq: 5, LossStart: 3, LossBurst: 2})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Lost != 2 {
		t.Fatalf("lost = %d", sess.Lost)
	}
	// 8 surviving packets plus the duplicate-free wire count.
	if len(sess.Wire) != 8 {
		t.Fatalf("wire = %d", len(sess.Wire))
	}
	if len(sess.Boundaries) != 8 {
		t.Fatalf("boundaries = %d", len(sess.Boundaries))
	}
}
