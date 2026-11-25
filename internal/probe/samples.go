package probe

import (
	"fmt"
	"io"
)

type readerAt interface {
	ReadAt([]byte, int64) (int, error)
}

func readNodePayload(f readerAt, n *boxNode) ([]byte, error) {
	if n == nil {
		return nil, fmt.Errorf("nil box")
	}
	size := n.payloadEnd - n.payloadStart
	if size < 0 || size > 16*1024*1024 {
		return nil, fmt.Errorf("payload size %d out of supported range", size)
	}
	b := make([]byte, size)
	got, err := f.ReadAt(b, n.payloadStart)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if int64(got) != size {
		return nil, fmt.Errorf("payload short read: %d/%d", got, size)
	}
	return b, nil
}

func needBytes(b []byte, off, n int) error {
	if off < 0 || n < 0 || off+n > len(b) {
		return fmt.Errorf("offset %d length %d exceeds payload %d", off, n, len(b))
	}
	return nil
}

type stscEntry struct {
	FirstChunk             uint32
	SamplesPerChunk        uint32
	SampleDescriptionIndex uint32
}

func parseSampleTables(f readerAt, t *Track, stbl *boxNode) error {
	stsz := findChild(stbl, "stsz")
	stts := findChild(stbl, "stts")
	stco := findChild(stbl, "stco")
	co64 := findChild(stbl, "co64")
	stsc := findChild(stbl, "stsc")
	if stsz == nil || stts == nil || (stco == nil && co64 == nil) {
		return fmt.Errorf("missing stsz, stts, or stco/co64")
	}
	sizeBytes, err := readNodePayload(f, stsz)
	if err != nil {
		return err
	}
	if err := needBytes(sizeBytes, 0, 12); err != nil {
		return err
	}
	commonSize := u32(sizeBytes, 4)
	n := int(u32(sizeBytes, 8))
	sizes := make([]uint32, n)
	if commonSize != 0 {
		for i := range sizes {
			sizes[i] = commonSize
		}
	} else {
		for i := 0; i < n; i++ {
			off := 12 + i*4
			if err := needBytes(sizeBytes, off, 4); err != nil {
				return fmt.Errorf("stsz entry %d: %w", i, err)
			}
			sizes[i] = u32(sizeBytes, off)
		}
	}

	durations, err := parseSTTS(f, stts, n)
	if err != nil {
		return err
	}
	comp, err := parseCTTS(f, findChild(stbl, "ctts"), n)
	if err != nil {
		return err
	}
	sync, err := parseSTSS(f, findChild(stbl, "stss"))
	if err != nil {
		return err
	}
	entries, err := parseSTSC(f, stsc)
	if err != nil {
		return err
	}
	offsets, err := parseOffsets(f, stco, co64)
	if err != nil {
		return err
	}

	t.Samples = make([]Sample, n)
	var tick int64
	for i := 0; i < n; i++ {
		t.Samples[i] = Sample{Index: i, Size: sizes[i], Duration: durations[i], IsSync: sync[i]}
		t.LastTick = tick + int64(durations[i])
		tick = t.LastTick
	}
	if len(comp) > 0 {
		t.CompositionOffsets = comp
		t.FirstTick = comp[0]
	}
	t.SyncSamples = map[int]bool{}
	for i, yes := range sync {
		if yes {
			t.SyncSamples[i] = true
		}
	}
	if err := assignOffsets(t.Samples, sizes, offsets, entries); err != nil {
		return err
	}
	return nil
}

func parseSTTS(f readerAt, n *boxNode, want int) ([]uint32, error) {
	b, err := readNodePayload(f, n)
	if err != nil {
		return nil, err
	}
	if err := needBytes(b, 0, 8); err != nil {
		return nil, err
	}
	count := int(u32(b, 4))
	out := make([]uint32, 0, want)
	off := 8
	for i := 0; i < count && len(out) < want; i++ {
		if err := needBytes(b, off, 8); err != nil {
			return nil, fmt.Errorf("stts entry %d: %w", i, err)
		}
		repeat := int(u32(b, off))
		delta := u32(b, off+4)
		for j := 0; j < repeat && len(out) < want; j++ {
			out = append(out, delta)
		}
		off += 8
	}
	if len(out) != want {
		return nil, fmt.Errorf("stts covers %d samples, stsz declares %d", len(out), want)
	}
	return out, nil
}

func parseCTTS(f readerAt, n *boxNode, want int) ([]int64, error) {
	if n == nil {
		return nil, nil
	}
	b, err := readNodePayload(f, n)
	if err != nil {
		return nil, err
	}
	if err := needBytes(b, 0, 8); err != nil {
		return nil, err
	}
	version, _ := fullBoxVersionFlags(b)
	count := int(u32(b, 4))
	out := make([]int64, 0, want)
	off := 8
	for i := 0; i < count && len(out) < want; i++ {
		if err := needBytes(b, off, 8); err != nil {
			return nil, fmt.Errorf("ctts entry %d: %w", i, err)
		}
		repeat := int(u32(b, off))
		var value int64
		if version == 1 {
			value = int64(u32(b, off+4)) // conversion of signed field is adequate for fixtures
			if value > 0x7fffffff {
				value -= 0x100000000
			}
		} else {
			value = int64(u32(b, off+4))
		}
		for j := 0; j < repeat && len(out) < want; j++ {
			out = append(out, value)
		}
		off += 8
	}
	return out, nil
}

func parseSTSS(f readerAt, n *boxNode) (map[int]bool, error) {
	out := map[int]bool{}
	if n == nil {
		return nil, nil
	}
	b, err := readNodePayload(f, n)
	if err != nil {
		return nil, err
	}
	if err := needBytes(b, 0, 8); err != nil {
		return nil, err
	}
	count := int(u32(b, 4))
	for i := 0; i < count; i++ {
		off := 8 + i*4
		if err := needBytes(b, off, 4); err != nil {
			return nil, fmt.Errorf("stss entry %d: %w", i, err)
		}
		number := int(u32(b, off))
		if number > 0 {
			out[number-1] = true
		}
	}
	return out, nil
}

func parseSTSC(f readerAt, n *boxNode) ([]stscEntry, error) {
	if n == nil {
		return nil, nil
	}
	b, err := readNodePayload(f, n)
	if err != nil {
		return nil, err
	}
	if err := needBytes(b, 0, 8); err != nil {
		return nil, err
	}
	count := int(u32(b, 4))
	out := make([]stscEntry, 0, count)
	for i := 0; i < count; i++ {
		off := 8 + i*12
		if err := needBytes(b, off, 12); err != nil {
			return nil, fmt.Errorf("stsc entry %d: %w", i, err)
		}
		out = append(out, stscEntry{FirstChunk: u32(b, off), SamplesPerChunk: u32(b, off+4), SampleDescriptionIndex: u32(b, off+8)})
	}
	return out, nil
}
