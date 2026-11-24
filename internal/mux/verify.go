package mux

import (
	"os"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/fault"
)

// VerifyReadable parses the MP4 box tree with go-mp4 and requires the core
// boxes needed by medpipe's reader.
func VerifyReadable(path string) *fault.Error {
	f, err := os.Open(path)
	if err != nil {
		return fault.Wrap(fault.ErrOutputUnreadable, "mux.verify.open", 0, "open output", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return fault.New(fault.ErrOutputUnreadable, "mux.verify.stat", 0, "output is empty")
	}
	paths := []gomp4.BoxPath{
		{gomp4.BoxTypeFtyp()},
		{gomp4.BoxTypeMoov()},
		{gomp4.BoxTypeMdat()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsz()},
	}
	boxes, err := gomp4.ExtractBoxes(f, nil, paths)
	if err != nil {
		return fault.Wrap(fault.ErrOutputUnreadable, "mux.verify.boxes", 0, "go-mp4 could not parse output", err)
	}
	seen := map[string]bool{}
	for _, b := range boxes {
		seen[b.Type.String()] = true
	}
	for _, typ := range []string{"ftyp", "moov", "mdat", "trak", "stsz"} {
		if !seen[typ] {
			return fault.New(fault.ErrOutputUnreadable, "mux.verify.boxes", 0, "missing MP4 box "+typ)
		}
	}
	return nil
}
