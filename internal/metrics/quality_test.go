package metrics

import (
	"crypto/sha256"
	"testing"

	"mediatool/internal/remux"
	"mediatool/internal/timeline"
)

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hexEncode(h[:])
}

func hexEncode(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexd[v>>4]
		out[i*2+1] = hexd[v&0xf]
	}
	return string(out)
}

// buildManifestAndEvents creates two perfectly aligned tracks with known
// payload hashes so every metric can be checked exactly.
func buildManifestAndEvents(t *testing.T) (*remux.Manifest, []*timeline.SampleEvent) {
	vPayload := []byte("video-payload-bytes-0001") // 24 bytes
	aPayload := []byte("audio-payload")            // 13 bytes
	vHash := sha256.Sum256(vPayload)
	aHash := sha256.Sum256(aPayload)

	manifest := &remux.Manifest{
		Slots: []remux.SlotManifest{
			{
				Slot: "video-main", TrackID: 1, MediaType: "video",
				CodecKey: "avc1", Timescale: 90000,
				Fragments: []remux.FragmentManifest{{
					OutputSeq: 0, OriginSeq: 0, CorrectedDTS: 0,
					Duration: 5760, PayloadBytes: len(vPayload), Timescale: 90000,
					SampleCount: 2, PayloadHashHex: hashHex(vPayload),
				}},
			},
			{
				Slot: "audio-main", TrackID: 2, MediaType: "audio",
				CodecKey: "mp4a", Timescale: 48000,
				Fragments: []remux.FragmentManifest{{
					OutputSeq: 0, OriginSeq: 0, CorrectedDTS: 0,
					Duration: 3072, PayloadBytes: len(aPayload), Timescale: 48000,
					SampleCount: 3, PayloadHashHex: hashHex(aPayload),
				}},
			},
		},
	}
	events := []*timeline.SampleEvent{
		{TrackID: 1, Seq: 0, PayloadHash: vHash},
		{TrackID: 2, Seq: 0, PayloadHash: aHash},
	}
	return manifest, events
}

func TestComputeHashMatchAndSamples(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	rep := Compute(manifest, events, []SlotTarget{
		{Slot: "video-main", TargetBitBPS: 3000}, // 24 B / 64 ms = 3000 bps
		{Slot: "audio-main", TargetBitBPS: 1625}, // 13 B / 64 ms = 1625 bps
	})
	if !rep.SampleHashMatch {
		t.Fatal("identical input/output hashes must match")
	}
	if rep.QualitySamples != 5 {
		t.Fatalf("quality samples = %d, want 5", rep.QualitySamples)
	}
}

func TestComputeBitrateDeviation(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	// Actual video = 24*8/0.064 = 3000 bps; audio = 13*8/0.064 = 1625 bps.
	rep := Compute(manifest, events, []SlotTarget{
		{Slot: "video-main", TargetBitBPS: 3000},
		{Slot: "audio-main", TargetBitBPS: 1625},
	})
	if rep.ActualBitrateBPS != 4625 {
		t.Fatalf("actual bps = %d, want 4625", rep.ActualBitrateBPS)
	}
	if rep.TargetBitrateBPS != 4625 || rep.BitrateDeviationPct != 0 {
		t.Fatalf("target/deviation wrong: %+v", rep)
	}

	// A target of 5000 against an actual 4625 yields -7.5% deviation.
	rep2 := Compute(manifest, events, []SlotTarget{
		{Slot: "video-main", TargetBitBPS: 3000},
		{Slot: "audio-main", TargetBitBPS: 2000}, // total target 5000
	})
	if rep2.BitrateDeviationPct != -7.5 {
		t.Fatalf("deviation = %f, want -7.5", rep2.BitrateDeviationPct)
	}
}

func TestComputeAlignmentExact(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	rep := Compute(manifest, events, nil)
	if rep.ManifestAlignmentMS != 0 {
		t.Fatalf("aligned tracks must report 0 ms, got %f", rep.ManifestAlignmentMS)
	}
}

func TestComputeAlignmentDetectsDrift(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	// Shift audio start by 96 units at 48 kHz = 2 ms.
	manifest.Slots[1].Fragments[0].CorrectedDTS = 96
	rep := Compute(manifest, events, nil)
	if rep.ManifestAlignmentMS != 2 {
		t.Fatalf("alignment = %f, want 2", rep.ManifestAlignmentMS)
	}
}

func TestComputeHashMismatchDetected(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	// Corrupt the recorded output hash for the video chunk.
	manifest.Slots[0].Fragments[0].PayloadHashHex = hashHex([]byte("tampered"))
	rep := Compute(manifest, events, nil)
	if rep.SampleHashMatch {
		t.Fatal("tampered output hash must be detected")
	}
}

func TestComputeRepeatedEventExcluded(t *testing.T) {
	manifest, events := buildManifestAndEvents(t)
	events = append(events, &timeline.SampleEvent{
		TrackID: 1, Seq: 0, Repeated: true,
		PayloadHash: sha256.Sum256([]byte("duplicate")),
	})
	rep := Compute(manifest, events, nil)
	if !rep.SampleHashMatch {
		t.Fatal("repeated input events must not participate in hash checks")
	}
}

func TestComputeEmptyManifestFailsHash(t *testing.T) {
	rep := Compute(&remux.Manifest{}, nil, nil)
	if rep.SampleHashMatch {
		t.Fatal("an empty output cannot be a hash match")
	}
}
