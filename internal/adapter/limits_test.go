package adapter

import (
	"testing"

	"archive-recovery/internal/media"
)

func TestH264OversizeNALRejected(t *testing.T) {
	// A scanner with a tiny cap must reject unit growth as recoverable,
	// proving overlong length/unit growth cannot exhaust memory.
	sp := NewAnnexBScanner(classifyH264, 64)
	big := append([]byte{0, 0, 0, 1, 0x65}, make([]byte, 128)...)
	_, _, err := sp.Write(big)
	if err == nil || media.ClassOf(err) != media.ClassRecoverable {
		t.Fatalf("oversize NAL must be recoverable error, got %v", err)
	}
}

func TestH265OversizeNALRejected(t *testing.T) {
	sp := NewAnnexBScanner(classifyH265, 32)
	big := append([]byte{0, 0, 0, 1, 0x26}, make([]byte, 96)...)
	_, _, err := sp.Write(big)
	if media.ClassOf(err) != media.ClassRecoverable {
		t.Fatalf("oversize HEVC NAL must be recoverable, got %v", err)
	}
}

func TestRegistryLatestAndCodecs(t *testing.T) {
	a, err := Latest("h265")
	if err != nil || a.Version() != H265Version {
		t.Fatalf("latest h265 lookup wrong: %v %v", a, err)
	}
	found := false
	for _, c := range SupportedCodecs() {
		if c == "av1" {
			found = true
		}
	}
	if !found {
		t.Fatal("av1 missing from supported codec list")
	}
}
