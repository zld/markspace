package fixture

import (
	"path/filepath"
	"testing"

	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

var testBitrates = []int{16, 32, 64}

func TestGenerateNormalAndReparse(t *testing.T) {
	root := t.TempDir()
	if err := GenerateNormal(root, testBitrates); err != nil {
		t.Fatal(err)
	}
	for _, kbps := range testBitrates {
		audioPath := filepath.Join(AudioDir(root), itoa(kbps)+".rtplog")
		st, err := ingest.ParseRTPFile(audioPath, media.AudioClockRate, kbps)
		if err != nil {
			t.Fatalf("audio %d: %v", kbps, err)
		}
		if len(st.Packets) != AudioPackets {
			t.Fatalf("audio packets = %d", len(st.Packets))
		}
		videoPath := filepath.Join(VideoDir(root), itoa(kbps)+".rtplog")
		vst, err := ingest.ParseRTPFile(videoPath, media.VideoClockRate, kbps)
		if err != nil {
			t.Fatalf("video %d: %v", kbps, err)
		}
		if len(vst.Packets) != VideoPackets {
			t.Fatalf("video packets = %d", len(vst.Packets))
		}
	}
	// Container metadata round-trips.
	if _, err := ingest.ReadMP4File(filepath.Join(MetaDir(root), "sample.mp4")); err != nil {
		t.Fatalf("mp4: %v", err)
	}
	if _, err := ingest.ReadMXFFile(filepath.Join(MetaDir(root), "sample.mxf")); err != nil {
		t.Fatalf("mxf: %v", err)
	}
}

func TestGenerateDamagedRecoverable(t *testing.T) {
	root := t.TempDir()
	if err := GenerateDamaged(root, testBitrates); err != nil {
		t.Fatal(err)
	}
	for tier, kbps := range testBitrates {
		st, err := ingest.ParseRTPFile(filepath.Join(AudioDir(root), itoa(kbps)+".rtplog"),
			media.AudioClockRate, kbps)
		if err != nil {
			t.Fatalf("damaged audio must still parse: %v", err)
		}
		reps, err := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
		if err != nil {
			t.Fatal(err)
		}
		if reps[0].MissingCount() == 0 || reps[0].RecoveredCount() == 0 {
			t.Fatalf("tier %d: expected gap recovery, missing=%d recovered=%d",
				kbps, reps[0].MissingCount(), reps[0].RecoveredCount())
		}
		if len(st.Duplicates) == 0 {
			t.Fatal("injected duplicate must be recorded")
		}
		if tier == 0 && len(st.IsolatedRanges()) == 0 {
			t.Fatal("injected jump must produce an isolated range")
		}

		vst, err := ingest.ParseRTPFile(filepath.Join(VideoDir(root), itoa(kbps)+".rtplog"),
			media.VideoClockRate, kbps)
		if err != nil {
			t.Fatalf("damaged video: %v", err)
		}
		if vst.Unwrapper.WrapCount() == 0 {
			t.Fatal("boundary-wrapping video must expand at least one rollover")
		}
	}
	// Damaged containers report locatable errors at their offsets.
	if _, err := ingest.ReadMP4File(filepath.Join(MetaDir(root), "sample.mp4")); err == nil {
		t.Fatal("truncated MP4 must fail")
	} else if _, ok := media.AsLocatable(err); !ok {
		t.Fatalf("mp4 error must be locatable: %v", err)
	}
	if _, err := ingest.ReadMXFFile(filepath.Join(MetaDir(root), "sample.mxf")); err == nil {
		t.Fatal("broken MXF must fail")
	} else if le, ok := media.AsLocatable(err); !ok || le.Code != "LENGTH_OUT_OF_BOUNDS" {
		t.Fatalf("mxf error = %v", err)
	}
}

func TestGenerateUnrecoverable(t *testing.T) {
	root := t.TempDir()
	if err := GenerateUnrecoverable(root, testBitrates); err != nil {
		t.Fatal(err)
	}
	st, err := ingest.ParseRTPFile(
		filepath.Join(AudioDir(root), itoa(testBitrates[0])+".rtplog"),
		media.AudioClockRate, testBitrates[0])
	if err != nil {
		t.Fatal(err)
	}
	reps, _ := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if len(reps[0].Unrecoverable) == 0 {
		t.Fatal("oversized gap must leave unrecoverable sequence numbers")
	}
	// The malformed sidecar cannot be parsed.
	_, err = ingest.ParseRTPFile(
		filepath.Join(AudioDir(root), itoa(testBitrates[0])+".rtplog.bad"),
		media.AudioClockRate, testBitrates[0])
	if err == nil {
		t.Fatal("malformed RTP must be rejected")
	}
}

func TestDeterministicRegeneration(t *testing.T) {
	root1, root2 := t.TempDir(), t.TempDir()
	if err := GenerateNormal(root1, testBitrates); err != nil {
		t.Fatal(err)
	}
	if err := GenerateNormal(root2, testBitrates); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(AudioDir(root1), itoa(testBitrates[0])+".rtplog")
	p2 := filepath.Join(AudioDir(root2), itoa(testBitrates[0])+".rtplog")
	if !filesEqual(p1, p2) {
		t.Fatal("regenerated fixtures must be byte-identical")
	}
}

func TestValidateBitrates(t *testing.T) {
	if err := validateBitrates(nil); err == nil {
		t.Fatal("empty bitrates must error")
	}
	if err := validateBitrates([]int{0}); err == nil {
		t.Fatal("zero bitrate must error")
	}
}
