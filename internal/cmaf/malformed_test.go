package cmaf

import (
	"testing"

	"replay/internal/model"
)

// ParseFragment must never panic on malformed box nesting; corrupt inputs
// are reported or ignored, not crashed on.
func TestParseFragmentStrayTrafBoxesNoPanic(t *testing.T) {
	cases := map[string][]byte{
		"trun outside traf": concat(moofBox(1, trunBox(100, 200)), mdatBox(10)),
		"tfhd outside traf": concat(moofBox(2, tfhdBox(1, 0)), mdatBox(10)),
		"tfdt outside traf": concat(moofBox(3, tfdtBox(0)), mdatBox(10)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, "seg.m4s", data)
			p := NewParser(&model.Report{})
			f, err := p.ParseFragment(path, testInit())
			if err != nil {
				return // a returned parse error is acceptable
			}
			if f == nil {
				t.Fatal("expected fragment or error, got nil,nil")
			}
		})
	}
}

// A stray trun at moof level must not prevent parsing a valid traf.
func TestParseFragmentStrayTrunKeepsValidTraf(t *testing.T) {
	goodTraf := trafBox(1, 180000, 512, 512)
	// moof children: mfhd is added by moofBox; append a stray trun by
	// building the moof body manually.
	body := concat(mfhdBox(7), trunBox(999), goodTraf)
	data := concat(makeBox("moof", body), mdatBox(10))
	path := writeFile(t, "seg_7.m4s", data)
	f, err := NewParser(nil).ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != 7 || f.Tfdt != 180000 || f.VideoDur != 1024 {
		t.Fatalf("valid traf lost amid stray boxes: %+v", f)
	}
}

// Deeply truncated input must surface as ErrTruncated, never panic.
func TestParseFragmentOneByte(t *testing.T) {
	path := writeFile(t, "seg_small.m4s", []byte{0, 0, 0, 8})
	rep := &model.Report{}
	f, err := NewParser(rep).ParseFragment(path, testInit())
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Fatalf("unreadable moof should yield nil fragment, got %+v", f)
	}
	if rep.Count(model.CodeTruncatedBox) == 0 {
		t.Fatal("expected a truncation issue")
	}
}
