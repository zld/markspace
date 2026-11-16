package mpd

import (
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	tpl := "seg-$RepresentationID$-$Number%05d$.m4s"
	got, err := Expand(tpl, SegmentRef{RepID: "r360", Number: 1, Time: 0})
	if err != nil {
		t.Fatal(err)
	}
	if want := "seg-r360-00001.m4s"; got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
	got, err = Expand("t-$Time$.m4s", SegmentRef{RepID: "r", Number: 2, Time: 3000})
	if err != nil || got != "t-3000.m4s" {
		t.Fatalf("time template = %q, %v", got, err)
	}
	got, err = Expand("price-$$5-$Number$.m4s", SegmentRef{Number: 7})
	if err != nil || got != "price-$5-7.m4s" {
		t.Fatalf("escaped dollar = %q, %v", got, err)
	}
}

func TestExpandErrors(t *testing.T) {
	ref := SegmentRef{RepID: "r", Number: 1}
	cases := []struct {
		name string
		tpl  string
		want string
	}{
		{"empty", "", "empty"},
		{"unterminated", "seg-$Number", "unterminated"},
		{"unknown", "seg-$Bandwidth$.m4s", "unknown placeholder"},
		{"bad spec", "seg-$Number%5d$.m4s", "invalid width"},
		{"zero width", "seg-$Number%00d$.m4s", "width"},
		{"width on repid", "seg-$RepresentationID%02d$.m4s", "does not accept"},
		{"traversal", "../$Number$.m4s", "traversal"},
		{"absolute", "/tmp/$Number$.m4s", "absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Expand(tc.tpl, ref)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Expand(%q) want error containing %q, got %v", tc.tpl, tc.want, err)
			}
		})
	}
}

func TestSafePathVariants(t *testing.T) {
	good := []string{"a/b.m4s", "dir/sub/file.m4s", "a..b.m4s", "..hidden.m4s"}
	for _, p := range good {
		if _, err := SafePath(p); err != nil {
			t.Errorf("SafePath(%q) rejected: %v", p, err)
		}
	}
	bad := []string{"a/../b", `a\..\b`, "./x", "x/./y", "a//..", "x/\x00y"}
	for _, p := range bad {
		if _, err := SafePath(p); err == nil {
			t.Errorf("SafePath(%q) should fail", p)
		}
	}
}

func TestExpandAllRejectsDuplicates(t *testing.T) {
	// A template ignoring both RepID and Number collides for every segment.
	refs := []SegmentRef{
		{RepID: "a", Number: 1},
		{RepID: "b", Number: 1},
	}
	if _, err := ExpandAll("fixed.m4s", refs); err == nil {
		t.Fatal("identical output paths must be rejected")
	} else if !strings.Contains(err.Error(), "duplicate output path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpandAllOrderAndCount(t *testing.T) {
	refs := []SegmentRef{
		{RepID: "r360", Number: 1, Time: 0},
		{RepID: "r360", Number: 2, Time: 100},
		{RepID: "r720", Number: 1, Time: 75},
	}
	out, err := ExpandAll("seg-$RepresentationID$-$Number%05d$.m4s", refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expanded %d, want 3", len(out))
	}
	want := []string{
		"seg-r360-00001.m4s",
		"seg-r360-00002.m4s",
		"seg-r720-00001.m4s",
	}
	for i, w := range want {
		if out[i].Path != w {
			t.Errorf("out[%d] = %q, want %q", i, out[i].Path, w)
		}
	}
}
