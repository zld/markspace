package fixtures

import (
	"testing"
)

// TestGenerateSpecs exercises every generated fixture class so the demo
// command and acceptance script rely on files this package really produces.
func TestGenerateSpecs(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"good.mp4", DefaultSpec("good.mp4")},
		{"good.mov", func() Spec { s := DefaultSpec("good.mov"); s.Corrupt = "mov"; return s }()},
		{"gap.mp4", func() Spec { s := DefaultSpec("gap.mp4"); s.FrameDropEvery = 20; return s }()},
		{"wrap.mp4", func() Spec {
			s := DefaultSpec("wrap.mp4")
			s.Tracks = s.Tracks[:1]
			s.WrapAt = 20
			return s
		}()},
		{"truncated.mp4", func() Spec { s := DefaultSpec("truncated.mp4"); s.TruncateBytes = 4000; return s }()},
		{"corrupt_box.mp4", func() Spec {
			s := DefaultSpec("corrupt_box.mp4")
			s.Corrupt = "corrupt_box"
			s.CorruptBox = "stco"
			return s
		}()},
		{"zero_duration.mp4", func() Spec { s := DefaultSpec("zero_duration.mp4"); s.ZeroDuration = true; return s }()},
	}
	for _, c := range cases {
		if _, err := Generate(c.spec, t.TempDir()); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}
