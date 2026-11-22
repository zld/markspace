package scan

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// buildTables validates every mandatory sample table and retains cursors for
// bounded batch walks. Entry counts are checked against the configured cap
// before any slice expansion, and every chunk offset is checked against the
// mdat extent so a corrupt table can never drive an out-of-bounds read.
func buildTables(r io.ReadSeeker, trak *mp4.BoxInfo, stbl *mp4.BoxInfo,
	fileSize uint64, cfg Config) (*sampleTables, uint32, error) {
	const op = "scan.stbl"

	stsdP, err := onePayload(r, stbl, mp4.BoxTypeStsd())
	if err != nil {
		return nil, 0, err
	}
	stsd := stsdP.(*mp4.Stsd)
	if stsd.EntryCount != 1 {
		return nil, 0, clock.ErrRecoverablef(op, "stsd entry_count=%d: only single-description tracks supported", stsd.EntryCount)
	}

	stszP, err := onePayload(r, stbl, mp4.BoxTypeStsz())
	if err != nil {
		return nil, 0, err
	}
	stsz := stszP.(*mp4.Stsz)
	if stsz.SampleCount > cfg.MaxTableEntries {
		return nil, 0, clock.ErrRecoverablef(op, "stsz sample_count %d exceeds cap %d", stsz.SampleCount, cfg.MaxTableEntries)
	}
	sizes, err := expandSizes(stsz)
	if err != nil {
		return nil, 0, err
	}

	sttsP, err := onePayload(r, stbl, mp4.BoxTypeStts())
	if err != nil {
		return nil, 0, err
	}
	stts := sttsP.(*mp4.Stts)
	if stts.EntryCount > cfg.MaxTableEntries {
		return nil, 0, clock.ErrRecoverablef(op, "stts entry_count %d exceeds cap", stts.EntryCount)
	}

	var cttsEntries []mp4.CttsEntry
	if cttsP, cerr := onePayload(r, stbl, mp4.BoxTypeCtts()); cerr == nil {
		cttsEntries = cttsP.(*mp4.Ctts).Entries
	}

	sync, err := extractSync(r, stbl, stsz.SampleCount, cfg)
	if err != nil {
		return nil, 0, err
	}
	chunks, err := buildChunks(r, stbl, fileSize, uint64(stsz.SampleCount), cfg)
	if err != nil {
		return nil, 0, err
	}

	tab := &sampleTables{
		sizes:  sizes,
		chunks: chunks,
		stts:   stts.Entries,
		ctts:   cttsEntries,
		sync:   sync,
	}
	return tab, stsz.SampleCount, nil
}

func expandSizes(stsz *mp4.Stsz) ([]uint32, error) {
	n := int(stsz.SampleCount)
	if n == 0 {
		return nil, clock.ErrRecoverablef("scan.stsz", "sample_count is zero")
	}
	sizes := make([]uint32, n)
	if stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = stsz.SampleSize
		}
		return sizes, nil
	}
	if len(stsz.EntrySize) != n {
		return nil, clock.ErrRecoverablef("scan.stsz", "entry_size len %d != sample_count %d", len(stsz.EntrySize), n)
	}
	copy(sizes, stsz.EntrySize)
	for i, sz := range sizes {
		if sz == 0 {
			return nil, clock.WithTrack(clock.ErrRecoverablef("scan.stsz", "sample %d has zero size", i+1), 0, int64(i))
		}
	}
	return sizes, nil
}

func extractSync(r io.ReadSeeker, stbl *mp4.BoxInfo, count uint32, cfg Config) (map[uint32]struct{}, error) {
	payloads, err := mp4.ExtractBoxWithPayload(r, stbl, mp4.BoxPath{mp4.BoxTypeStss()})
	if err != nil {
		// No stss box at all: every sample is a sync sample per ISO 14496-12.
		sync := make(map[uint32]struct{}, count)
		for n := uint32(1); n <= count; n++ {
			sync[n] = struct{}{}
		}
		return sync, nil
	}
	stss := payloads[0].Payload.(*mp4.Stss)
	if stss.EntryCount > cfg.MaxTableEntries {
		return nil, clock.ErrRecoverablef("scan.stss", "entry_count %d exceeds cap", stss.EntryCount)
	}
	if int(stss.EntryCount) != len(stss.SampleNumber) {
		return nil, clock.ErrRecoverablef("scan.stss", "entry_count %d != rows %d",
			stss.EntryCount, len(stss.SampleNumber))
	}
	sync := make(map[uint32]struct{}, stss.EntryCount)
	var last uint32
	for i, num := range stss.SampleNumber {
		if num == 0 || num > count {
			return nil, clock.WithTrack(clock.ErrRecoverablef("scan.stss", "sync number %d out of range [1,%d]", num, count), 0, int64(i))
		}
		if i > 0 && num <= last {
			return nil, clock.ErrRecoverablef("scan.stss", "sync numbers not strictly increasing at %d", num)
		}
		last = num
		sync[num] = struct{}{}
	}
	// An explicit empty stss means no sync samples, not "all sync".
	return sync, nil
}
