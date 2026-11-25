package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mp4 "github.com/abema/go-mp4"

	"replay/internal/cmaf"
)

// rewriteBox locates the first box of type want in path, unmarshals it
// with go-mp4, applies mutate and writes the box back in place. The box
// size must not change.
func rewriteBox(path string, want mp4.BoxType, mutate func(b mp4.IBox)) error {
	p := cmaf.NewParser(nil)
	var target *mp4.BoxInfo
	if err := p.WalkBoxes(path, func(bi *mp4.BoxInfo) error {
		if bi.Type == want && target == nil {
			t := *bi
			target = &t
		}
		return nil
	}); err != nil {
		return err
	}
	if target == nil {
		return fmt.Errorf("variants: no %s box in %s", want, path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	payloadOff := int64(target.Offset) + int64(target.HeaderSize)
	payloadSize := target.Size - target.HeaderSize
	var box mp4.IBox
	switch want {
	case mp4.BoxTypeMfhd():
		box = &mp4.Mfhd{}
	case mp4.BoxTypeTfdt():
		box = &mp4.Tfdt{}
	default:
		return fmt.Errorf("variants: unsupported box type %s", want)
	}
	if _, err := f.Seek(payloadOff, 0); err != nil {
		return err
	}
	if _, err := mp4.Unmarshal(f, payloadSize, box, mp4.Context{}); err != nil {
		return err
	}
	mutate(box)
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, box.(mp4.IImmutableBox), mp4.Context{}); err != nil {
		return err
	}
	if uint64(buf.Len()) != payloadSize {
		return fmt.Errorf("variants: %s payload size changed (%d != %d)", want, buf.Len(), payloadSize)
	}
	_, err = f.WriteAt(buf.Bytes(), payloadOff)
	return err
}

// segmentNames lists the .m4s files of a set directory, sorted.
func segmentNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".m4s") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// makeGapSet copies src and shifts mfhd sequence numbers of the 4th
// segment onward up by one, leaving a sequence-number gap.
func makeGapSet(src, dst string) error {
	if err := copySet(src, dst); err != nil {
		return err
	}
	names, err := segmentNames(dst)
	if err != nil {
		return err
	}
	for i, n := range names {
		if i < 3 {
			continue
		}
		if err := rewriteBox(join(dst, n), mp4.BoxTypeMfhd(), func(b mp4.IBox) {
			b.(*mp4.Mfhd).SequenceNumber++
		}); err != nil {
			return err
		}
	}
	return nil
}

// makeTfdtRegressSet copies src and rewinds the 6th segment's tfdt by 2s
// (in the video track's own timescale), creating a decode-time regression.
func makeTfdtRegressSet(src, dst string) error {
	if err := copySet(src, dst); err != nil {
		return err
	}
	p := cmaf.NewParser(nil)
	init, err := p.ParseInit(join(dst, "init.mp4"))
	if err != nil {
		return err
	}
	v := init.Video()
	if v == nil {
		return fmt.Errorf("variants: no video track in %s", dst)
	}
	rewind := uint64(2 * v.Timescale) // 2 seconds in media units
	names, err := segmentNames(dst)
	if err != nil {
		return err
	}
	if len(names) < 6 {
		return fmt.Errorf("variants: need at least 6 segments, got %d", len(names))
	}
	return rewriteBox(join(dst, names[5]), mp4.BoxTypeTfdt(), func(b mp4.IBox) {
		t := b.(*mp4.Tfdt)
		if t.Version == 1 {
			t.BaseMediaDecodeTimeV1 -= rewind
		} else {
			t.BaseMediaDecodeTimeV0 -= uint32(rewind)
		}
	})
}

// makeDupSet copies src and duplicates the 2nd segment file, producing a
// duplicate mfhd sequence number in file order.
func makeDupSet(src, dst string) error {
	if err := copySet(src, dst); err != nil {
		return err
	}
	names, err := segmentNames(dst)
	if err != nil {
		return err
	}
	if len(names) < 2 {
		return fmt.Errorf("variants: need at least 2 segments, got %d", len(names))
	}
	base := strings.TrimSuffix(names[1], ".m4s")
	return copyFile(join(dst, base+"_dup.m4s"), join(dst, names[1]))
}

// makeTruncatedSet copies src and cuts the 5th segment at 40% of its
// size, leaving a truncated box.
func makeTruncatedSet(src, dst string) error {
	if err := copySet(src, dst); err != nil {
		return err
	}
	names, err := segmentNames(dst)
	if err != nil {
		return err
	}
	if len(names) < 5 {
		return fmt.Errorf("variants: need at least 5 segments, got %d", len(names))
	}
	p := join(dst, names[4])
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.Truncate(p, st.Size()*2/5)
}

// ensure filepath stays imported when helpers move between files.
var _ = filepath.Join
