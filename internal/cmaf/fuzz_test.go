package cmaf

import (
	"testing"

	"replay/internal/model"
)

func FuzzParseFragmentNoPanic(f *testing.F) {
	f.Add([]byte{})
	f.Add(moofBox(1, trafBox(1, 0, 100)))
	f.Add(concat(moofBox(1, trafBox(1, 0, 100)), mdatBox(50)))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFile(t, "fuzz.m4s", data)
		p := NewParser(&model.Report{})
		_, _ = p.ParseFragment(path, testInit())
	})
}

func FuzzParseInitNoPanic(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("xxxx"))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFile(t, "fuzz-init.mp4", data)
		p := NewParser(&model.Report{})
		_, _ = p.ParseInit(path)
	})
}
