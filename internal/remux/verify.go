package remux

import (
	"os"

	"mediapack/internal/maptrack"
	"mediapack/internal/model"
)

// verify reopens the staged output and checks every manifest record: the bytes
// at TargetOffset must equal the source sample payload exactly. It returns the
// total number of differing bytes (0 means lossless).
func verify(staged string, m *maptrack.Mapping, mf *Manifest) (int64, error) {
	f, err := os.Open(staged)
	if err != nil {
		return 0, model.NewError(model.CodeVerify,
			"cannot reopen staged output: "+err.Error()).WithFile(staged)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, model.NewError(model.CodeVerify,
			"cannot stat staged output: "+err.Error()).WithFile(staged)
	}

	// Index source payloads by (target track, sample index) for comparison.
	type key struct {
		track uint32
		index uint32
	}
	src := map[key][]byte{}
	for _, b := range m.Bindings {
		for _, s := range b.Samples {
			src[key{b.TargetTrack, s.Index}] = s.Payload
		}
	}

	var diff int64
	buf := make([]byte, 0, 4096)
	for _, rec := range mf.Records {
		if rec.TargetOffset < 0 ||
			rec.TargetOffset+int64(rec.Size) > info.Size() {
			return 0, model.NewError(model.CodeVerify,
				"target offset outside output bounds").
				WithFile(staged).WithTrack(rec.TargetTrack).
				WithOffset(rec.TargetOffset)
		}
		want := src[key{rec.TargetTrack, rec.Index}]
		if len(want) != int(rec.Size) {
			return 0, model.NewError(model.CodeVerify,
				"manifest size disagrees with source payload").
				WithFile(staged).WithTrack(rec.TargetTrack)
		}
		if cap(buf) < int(rec.Size) {
			buf = make([]byte, rec.Size)
		}
		got := buf[:rec.Size]
		if _, err := f.ReadAt(got, rec.TargetOffset); err != nil {
			return 0, model.NewError(model.CodeVerify,
				"cannot read output sample: "+err.Error()).
				WithFile(staged).WithTrack(rec.TargetTrack).
				WithOffset(rec.TargetOffset)
		}
		diff += byteDiff(got, want)
	}
	return diff, nil
}

func byteDiff(a, b []byte) int64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var d int64
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			d++
		}
	}
	d += int64(len(a)-n) + int64(len(b)-n)
	return d
}
