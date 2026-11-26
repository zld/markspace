// Package index scans fMP4 fragments and maintains the summary cache. A scan
// records, per segment, its byte size and SHA-256, the samples it contains
// with raw ticks and canonical positions, and which samples are sync frames.
// The same code path classifies structural faults as segment-truncated or
// box-parse, so the streaming runner never has to reinterpret a file.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"medtrace/internal/core"

	"github.com/abema/go-mp4"
)

// mp4Scan is the parsed result of one fragment, before epoch assignment.
type mp4Scan struct {
	trackID   uint32
	timescale uint32
	samples   []rawSample
	// truncated reports the declared boxes ran past EOF.
	truncated bool
	// damageByte is the offset at which parsing stopped, for truncation.
	damageByte int64
}

// rawSample is one sample with raw ticks and a file offset.
type rawSample struct {
	offset int64
	size   uint32
	dts    int64
	cts    int64
	dur    int64 // ticks
	key    bool
}

// scanFragment opens rel (root-relative) and parses its moof/traf/trun
// structure. It computes the file hash in the same pass. A structural problem
// yields a partially filled result and a *core.Fault whose kind is
// segment-truncated or box-parse; the caller decides whether that is fatal.
func scanFragment(root, rel string) (*mp4Scan, string, int64, *core.Fault) {
	abs, err := core.SafeJoin(root, rel)
	if err != nil {
		return nil, "", 0, core.NewFault(core.KindPath, core.Pos{File: rel, Byte: -1}, "scan", err.Error())
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "", 0, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "scan", "open fragment")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, "", 0, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "scan", "stat fragment")
	}
	size := st.Size()

	// Hash and parse from independent cursors: hashing must see the whole
	// file even when parsing stops early at damage.
	hash, err := hashFile(abs)
	if err != nil {
		return nil, "", 0, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "scan", "hash fragment")
	}

	sc, pf := parseFragment(f, rel, size)
	return sc, hash, size, pf
}

// parseFragment walks top-level boxes and unmarshals each moof's traf/trun.
func parseFragment(f io.ReadSeeker, rel string, size int64) (*mp4Scan, *core.Fault) {
	out := &mp4Scan{}
	pos := func(off int64) core.Pos { return core.Pos{File: rel, Byte: off} }
	var sawMoof bool
	for {
		start, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, core.WrapFault(err, core.KindIO, pos(start), "scan", "seek")
		}
		if start >= size {
			break
		}
		if size-start < 8 {
			out.truncated, out.damageByte = true, start
			return out, core.NewFault(core.KindSegmentTruncated, pos(start), "scan",
				fmt.Sprintf("%d trailing bytes cannot hold a box header", size-start))
		}
		bi, err := mp4.ReadBoxInfo(f)
		if err != nil {
			out.truncated, out.damageByte = true, start
			return out, core.WrapFault(err, core.KindSegmentTruncated, pos(start), "scan", "read box header")
		}
		if bi.Type == mp4.BoxTypeMoof() {
			sawMoof = true
			if f2 := parseMoof(f, rel, bi, out); f2 != nil {
				return out, f2
			}
		}
		if _, err := bi.SeekToEnd(f); err != nil {
			out.truncated, out.damageByte = true, start
			return out, core.WrapFault(err, core.KindSegmentTruncated, pos(start), "scan", "seek past box")
		}
	}
	if !sawMoof {
		return out, core.NewFault(core.KindBoxParse, pos(0), "scan", "fragment contains no moof box")
	}
	return out, nil
}

// parseMoof extracts the default sample table and every trun entry, walking
// the mdat data in order. Only one traf (one track) per fragment is supported;
// a second is reported because the index is per segment per track.
func parseMoof(f io.ReadSeeker, rel string, moof *mp4.BoxInfo, out *mp4Scan) *core.Fault {
	pos := core.Pos{File: rel, Byte: int64(moof.Offset)}
	bips, err := mp4.ExtractBoxesWithPayload(f, moof, []mp4.BoxPath{
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()},
		{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()},
	})
	if err != nil {
		out.truncated = true
		out.damageByte = int64(moof.Offset)
		return core.WrapFault(err, core.KindSegmentTruncated, pos, "scan", "parse moof")
	}
	var tfhd *mp4.Tfhd
	var tfdt *mp4.Tfdt
	var truns []*mp4.Trun
	for _, b := range bips {
		switch p := b.Payload.(type) {
		case *mp4.Tfhd:
			if tfhd != nil {
				return core.NewFault(core.KindBoxParse, core.Pos{File: rel, Byte: int64(b.Info.Offset)}, "scan", "multiple tfhd in one moof")
			}
			tfhd = p
		case *mp4.Tfdt:
			tfdt = p
		case *mp4.Trun:
			truns = append(truns, p)
		}
	}
	if tfhd == nil {
		return core.NewFault(core.KindBoxParse, pos, "scan", "moof has no tfhd")
	}
	if out.trackID != 0 && out.trackID != tfhd.TrackID {
		return core.NewFault(core.KindBoxParse, pos, "scan", "fragment mixes tracks")
	}
	out.trackID = tfhd.TrackID
	if len(truns) == 0 {
		return core.NewFault(core.KindBoxParse, pos, "scan", "moof has no trun")
	}

	base := int64(moof.Offset) + int64(moof.Size)
	if tfhd.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
		base = int64(tfhd.BaseDataOffset)
	}
	dts := int64(0)
	if tfdt != nil {
		dts = int64(tfdt.GetBaseMediaDecodeTime())
	}
	cursor := base
	for _, tr := range truns {
		if tr.CheckFlag(0x000001) {
			cursor = base + int64(tr.DataOffset)
		}
		defaultDur := int64(tfhd.DefaultSampleDuration)
		defaultSize := int64(tfhd.DefaultSampleSize)
		defaultFlags := tfhd.DefaultSampleFlags
		for i := 0; i < len(tr.Entries); i++ {
			e := tr.Entries[i]
			dur := defaultDur
			if tr.CheckFlag(0x000100) {
				dur = int64(e.SampleDuration)
			}
			sz := defaultSize
			if tr.CheckFlag(0x000200) {
				sz = int64(e.SampleSize)
			}
			flags := defaultFlags
			if tr.CheckFlag(0x000400) {
				flags = e.SampleFlags
			} else if i == 0 && tr.CheckFlag(0x000004) {
				flags = tr.FirstSampleFlags
			}
			cts := dts
			if tr.CheckFlag(0x000800) {
				cts = dts + tr.GetSampleCompositionTimeOffset(i)
			}
			key := flags&0x00010000 == 0 // sample_is_non_sync_sample
			out.samples = append(out.samples, rawSample{
				offset: cursor, size: uint32(sz), dts: dts, cts: cts, dur: dur, key: key,
			})
			cursor += sz
			dts += dur
		}
	}
	return nil
}

// hashFile returns the lowercase hex SHA-256 of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
