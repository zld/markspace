package fixture

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"hlsflow/internal/ts"
)

func TestGeneratedStreamParses(t *testing.T) {
	data := segmentTS(0, 0)
	if len(data)%ts.PacketSize != 0 {
		t.Fatalf("stream not packet aligned: %d bytes", len(data))
	}
	p := ts.NewParser(bytes.NewReader(data))
	var idrPackets int
	var sawPES bool
	for {
		pkt, err := p.Read()
		if errors.Is(err, ts.ErrTruncated) {
			t.Fatal("fixture stream must end on a packet boundary")
		}
		if err != nil {
			break
		}
		if pkt.PID == videoPID && pkt.PUSI && len(pkt.Payload) > 0 {
			info, perr := ts.ParsePES(pkt.Payload)
			if perr == nil && info.HasPTS {
				sawPES = true
				if ts.ContainsIDR(info.Data) {
					idrPackets++
				}
			}
		}
	}
	if err := p.ValidatePSI(); err != nil {
		t.Fatal(err)
	}
	if p.VideoPID() != videoPID || p.AudioPID() != audioPID {
		t.Fatalf("pids video=%#x audio=%#x", p.VideoPID(), p.AudioPID())
	}
	if !sawPES {
		t.Fatal("no timestamped PES parsed")
	}
	if idrPackets != 1 {
		t.Fatalf("expected exactly one IDR per segment, got %d", idrPackets)
	}
}

func TestGenerateCleanAndFaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fx")
	res, err := Generate(dir, ScenarioFaults, []int{800000, 1800000, 3500000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Renditions) != 3 {
		t.Fatalf("renditions = %d", len(res.Renditions))
	}
	// The truncated file must actually be misaligned.
	tPath := filepath.Join(res.InputRoot, "inbox", "480p", "seg-00003.ts")
	tData, err := readFile(tPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tData)%ts.PacketSize == 0 {
		t.Fatal("truncation fixture is still packet aligned")
	}
	clean, err := Generate(filepath.Join(t.TempDir(), "clean"), ScenarioClean, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Renditions) != 3 {
		t.Fatalf("default ladder = %d", len(clean.Renditions))
	}
}

func TestRejectsBadRates(t *testing.T) {
	if _, err := Generate(filepath.Join(t.TempDir(), "x"), ScenarioClean, []int{0}); err == nil {
		t.Fatal("expected error for zero rate")
	}
	if _, err := Generate(filepath.Join(t.TempDir(), "x"), ScenarioClean, []int{100, 100}); err == nil {
		t.Fatal("expected error for duplicate rate")
	}
}

func readFile(path string) ([]byte, error) {
	return readFileOS(path)
}
