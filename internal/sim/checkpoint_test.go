package sim

import "testing"

// TestCorruptedCheckpointDetected proves a tampered checkpoint cannot silently
// produce the same result as an uninterrupted run. The CLI maps this class of
// divergence onto exit code 4 (recovery state inconsistency).
func TestCorruptedCheckpointDetected(t *testing.T) {
	cfg := testCfg()
	direct, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}

	sender := NewSender(cfg)
	pkts := sender.Generate()
	senderRefs = sender.RefByExpandedSeq

	first := NewEngine(cfg)
	cut := 250
	for i, rp := range pkts {
		if i == cut {
			break
		}
		first.ProcessPacket(rp)
	}
	snap, err := first.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the session hash chain inside the checkpoint.
	if len(snap.Session.Chain) == 0 {
		t.Fatal("checkpoint carries no hash chain")
	}
	snap.Session.Chain[0] ^= 0xFF

	restored, err := RestoreEngine(snap)
	if err != nil {
		// A hard restore failure is itself a valid checkpoint rejection.
		return
	}
	for i := cut; i < len(pkts); i++ {
		restored.ProcessPacket(pkts[i])
	}
	restored.Flush()
	tampered := restored.Finish()
	if tampered.EventSHA256 == direct.EventSHA256 {
		t.Fatal("tampered checkpoint reproduced the uninterrupted hash")
	}
}

// TestCheckpointPCMContinuity verifies audio sample counts form one unbroken
// run across a recovery boundary when the checkpoint is intact.
func TestCheckpointPCMContinuity(t *testing.T) {
	cfg := testCfg()
	sender := NewSender(cfg)
	pkts := sender.Generate()
	senderRefs = sender.RefByExpandedSeq

	cut := 300
	first := NewEngine(cfg)
	for i, rp := range pkts {
		if i == cut {
			break
		}
		first.ProcessPacket(rp)
	}
	snap, err := first.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreEngine(snap)
	if err != nil {
		t.Fatal(err)
	}
	for i := cut; i < len(pkts); i++ {
		restored.ProcessPacket(pkts[i])
	}
	restored.Flush()
	rep := restored.Finish()

	direct, _ := Run(cfg)
	if rep.AudioSamples != direct.AudioSamples {
		t.Fatalf("audio samples discontinuous: %d vs direct %d",
			rep.AudioSamples, direct.AudioSamples)
	}
}
