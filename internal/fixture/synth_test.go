package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"hlsforge/internal/cmaf"
)

func TestGenerateDeterministicAndParsable(t *testing.T) {
	root := t.TempDir()
	metas, err := Generate(root, []int{800000, 1600000}, Options{
		Seed: 7, PartMs: 500, DurationMs: 12000,
		Faults: []string{"missing", "order", "drift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(metas))
	}
	lo, hi := metas[0], metas[1]
	if len(lo.Present) != 24 || lo.DriftMs != DriftMs {
		t.Fatalf("low track: present=%d drift=%d", len(lo.Present), lo.DriftMs)
	}
	if len(hi.Present) != 23 || len(hi.Missing) != 1 || hi.Missing[0] != 13 {
		t.Fatalf("high track: present=%d missing=%v", len(hi.Present), hi.Missing)
	}
	// The order fault adds one duplicate delivery entry.
	if len(lo.Delivered) != 25 || len(hi.Delivered) != 24 {
		t.Fatalf("delivery lines: lo=%d hi=%d", len(lo.Delivered), len(hi.Delivered))
	}
	// Re-running with the same seed yields the same delivery order.
	root2 := t.TempDir()
	metas2, err := Generate(root2, []int{800000, 1600000}, Options{
		Seed: 7, PartMs: 500, DurationMs: 12000,
		Faults: []string{"missing", "order", "drift"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range metas {
		if len(metas[i].Delivered) != len(metas2[i].Delivered) {
			t.Fatal("delivery order not deterministic")
		}
		for j := range metas[i].Delivered {
			if metas[i].Delivered[j] != metas2[i].Delivered[j] {
				t.Fatal("delivery order differs across runs")
			}
		}
	}
	// Every emitted fragment must survive the strict CMAF parser.
	for _, m := range metas {
		init, err := os.ReadFile(filepath.Join(m.Dir, InitName()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := cmaf.ParseInit(init)
		if err != nil {
			t.Fatalf("init parse: %v", err)
		}
		for _, seq := range m.Present {
			raw, err := os.ReadFile(filepath.Join(m.Dir, FragmentName(seq)))
			if err != nil {
				t.Fatal(err)
			}
			frag, err := cmaf.ParseFragment(raw, info.Timescale)
			if err != nil {
				t.Fatalf("fragment %s parse: %v", FragmentName(seq), err)
			}
			if int(frag.Seq) != seq || int(frag.Duration) != 500 {
				t.Fatalf("fragment %d: seq=%d dur=%d", seq, frag.Seq, frag.Duration)
			}
		}
	}
}

func TestGenerateNoFaults(t *testing.T) {
	root := t.TempDir()
	metas, err := Generate(root, []int{800000}, Options{
		Seed: 1, PartMs: 500, DurationMs: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := metas[0]
	if len(m.Present) != 4 || len(m.Delivered) != 4 || m.DriftMs != 0 {
		t.Fatalf("unexpected meta: %+v", m)
	}
	if m.Present[0] != 1 || m.Present[3] != 4 {
		t.Fatal("present sequences must stay sorted")
	}
}

func TestParseFragmentName(t *testing.T) {
	seq, ok := ParseFragmentName("/tmp/frag-00007.mp4")
	if !ok || seq != 7 {
		t.Fatalf("got %d %v", seq, ok)
	}
	if _, ok := ParseFragmentName("random.bin"); ok {
		t.Fatal("non-fragment name must fail")
	}
}
