package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateNormalDeterministic(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	cfgA := Config{Root: filepath.Join(dirA, "gen"), Case: CaseNormal, Representations: 2, Fragments: 5}
	cfgB := Config{Root: filepath.Join(dirB, "gen"), Case: CaseNormal, Representations: 2, Fragments: 5}
	descsA, err := Generate(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(cfgB); err != nil {
		t.Fatal(err)
	}
	for _, d := range descsA {
		a := filepath.Join(dirA, "gen", d.ID)
		b := filepath.Join(dirB, "gen", d.ID)
		if !dirsEqual(t, a, b) {
			t.Fatalf("representation %s is not deterministic across runs", d.ID)
		}
	}
}

func TestGenerateFaultCases(t *testing.T) {
	cases := []Case{CaseTruncatedBox, CaseTFDTRegression, CaseMfhdGap, CaseDuplicateConflict, CaseDuplicateFold, CaseMisaligned}
	for _, c := range cases {
		t.Run(string(c), func(t *testing.T) {
			cfg := Config{Root: filepath.Join(t.TempDir(), "gen"), Case: c, Representations: 2, Fragments: 5}
			descs, err := Generate(cfg)
			if err != nil {
				t.Fatalf("case %s: %v", c, err)
			}
			names, err := listFragments(descs[0].Directory)
			if err != nil {
				t.Fatal(err)
			}
			switch c {
			case CaseMfhdGap:
				if len(names) != 4 {
					t.Fatalf("gap case should remove one fragment, got %d", len(names))
				}
			case CaseDuplicateConflict, CaseDuplicateFold:
				if len(names) != 6 {
					t.Fatalf("duplicate case should add one arrival, got %d", len(names))
				}
			default:
				if len(names) != 5 {
					t.Fatalf("case %s should keep five fragments, got %d", c, len(names))
				}
			}
			if _, err := os.Stat(filepath.Join(descs[0].Directory, RTPFileName)); err != nil {
				t.Fatalf("rtp stream missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(descs[0].Directory, "init.mp4")); err != nil {
				t.Fatalf("init segment missing: %v", err)
			}
		})
	}
}

func TestRejectsBadConfig(t *testing.T) {
	if _, err := Generate(Config{Root: t.TempDir(), Representations: 0, Fragments: 3}); err == nil {
		t.Fatal("expected error for zero representations")
	}
	if _, err := Generate(Config{Root: t.TempDir(), Representations: 2, Fragments: 1}); err == nil {
		t.Fatal("expected error for fragment count below 2")
	}
}
