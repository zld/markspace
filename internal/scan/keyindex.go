package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"regress/internal/fault"
)

// KeyIndexMagic is the four-byte prefix of every generated keyframe index
// file ("KIDX"), followed by a one byte format version.
var KeyIndexMagic = [4]byte{'K', 'I', 'D', 'X'}

const keyIndexVersion byte = 1

// IndexEntry is one externally supplied keyframe pointer.
type IndexEntry struct {
	Offset       int64  // byte offset of the access unit start code / OBU
	TimeTicks    uint64 // presentation time in the source clock
	FrameOrdinal int    // ordinal frame number, zero based
}

// KeyIndex is the parsed content of a .kidx file. TimeDen is the source
// clock denominator (ticks per second).
type KeyIndex struct {
	TimeDen uint64
	Entries []IndexEntry
}

// MarshalKeyIndex serializes an index in the deterministic on-disk format:
// magic "KIDX", version byte, uint32 time denominator, uint32 entry count,
// then 24 bytes per entry (uint64 offset, uint64 time ticks, uint32 frame
// ordinal, uint32 reserved zero).
func MarshalKeyIndex(idx *KeyIndex) ([]byte, error) {
	if idx.TimeDen == 0 {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "key index has zero time denominator")
	}
	var buf bytes.Buffer
	buf.Write(KeyIndexMagic[:])
	buf.WriteByte(keyIndexVersion)
	if idx.TimeDen > uint64(^uint32(0)) {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "time denominator exceeds uint32")
	}
	if err := binary.Write(&buf, binary.BigEndian, uint32(idx.TimeDen)); err != nil {
		return nil, err
	}
	if uint64(len(idx.Entries)) > uint64(^uint32(0)) {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "key index entry count overflow")
	}
	if err := binary.Write(&buf, binary.BigEndian, uint32(len(idx.Entries))); err != nil {
		return nil, err
	}
	for _, e := range idx.Entries {
		if err := binary.Write(&buf, binary.BigEndian, uint64(e.Offset)); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.BigEndian, e.TimeTicks); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.BigEndian, uint32(e.FrameOrdinal)); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.BigEndian, uint32(0)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// UnmarshalKeyIndex parses the on-disk format, rejecting truncated files.
func UnmarshalKeyIndex(r io.Reader) (*KeyIndex, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "cannot read key index magic").Wrap(err)
	}
	if magic != KeyIndexMagic {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "",
			fmt.Sprintf("bad key index magic %q", string(magic[:])))
	}
	var version [1]byte
	if _, err := io.ReadFull(r, version[:]); err != nil {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated key index version").Wrap(err)
	}
	if version[0] != keyIndexVersion {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "",
			fmt.Sprintf("unsupported key index version %d", version[0]))
	}
	var timeDen, count uint32
	if err := binary.Read(r, binary.BigEndian, &timeDen); err != nil {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated time denominator").Wrap(err)
	}
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated entry count").Wrap(err)
	}
	idx := &KeyIndex{TimeDen: uint64(timeDen), Entries: make([]IndexEntry, 0, count)}
	for i := uint32(0); i < count; i++ {
		var off, ticks uint64
		var ordinal, reserved uint32
		if err := binary.Read(r, binary.BigEndian, &off); err != nil {
			return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated entry offset").Wrap(err)
		}
		if err := binary.Read(r, binary.BigEndian, &ticks); err != nil {
			return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated entry timestamp").Wrap(err)
		}
		if err := binary.Read(r, binary.BigEndian, &ordinal); err != nil {
			return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated entry ordinal").Wrap(err)
		}
		if err := binary.Read(r, binary.BigEndian, &reserved); err != nil {
			return nil, fault.New(fault.StageScan, fault.CodeParse, "", "truncated entry reserved word").Wrap(err)
		}
		idx.Entries = append(idx.Entries, IndexEntry{
			Offset:       int64(off),
			TimeTicks:    ticks,
			FrameOrdinal: int(ordinal),
		})
	}
	return idx, nil
}

// IndexCheck is the verdict for one index entry.
type IndexCheck struct {
	Entry    IndexEntry
	EntryNum int
	OK       bool
	Code     fault.Code
	Detail   string
}

// IndexReport is the full validation result for an index against a scan.
type IndexReport struct {
	Total    int
	Accepted int
	Rejected int
	Checks   []IndexCheck
}

// ValidateIndex cross-checks every index entry against the parsed stream.
// Every discrepancy produces a structured fault; nothing is silently fixed.
// The report is returned together with an aggregated fault.Aggregate error so
// callers can both inspect the per-entry verdicts and fail the pipeline.
func ValidateIndex(res *ScanResult, idx *KeyIndex) (*IndexReport, error) {
	report := &IndexReport{Total: len(idx.Entries)}
	var errs []error
	rapByOffset := make(map[int64]int, len(res.Keyframes))
	for _, kfi := range res.Keyframes {
		rapByOffset[res.AccessUnits[kfi].Offset] = kfi
	}
	seenTimes := make(map[uint64]int)
	seenOrdinals := make(map[int]int)
	for i, e := range idx.Entries {
		chk := IndexCheck{Entry: e, EntryNum: i, OK: true}
		// Time ordering is validated first and every entry occupies its time
		// slot regardless of whether its offset is valid, so an out-of-range
		// entry cannot mask a later timestamp duplicate.
		if prev, dup := seenTimes[e.TimeTicks]; dup {
			chk.OK, chk.Code = false, fault.CodeIndexTimeDuplicate
			chk.Detail = fmt.Sprintf("timestamp %d repeats entry %d", e.TimeTicks, prev)
		} else if prev, dup := seenOrdinals[e.FrameOrdinal]; dup {
			chk.OK, chk.Code = false, fault.CodeIndexTimeDuplicate
			chk.Detail = fmt.Sprintf("frame ordinal %d repeats entry %d", e.FrameOrdinal, prev)
		}
		seenTimes[e.TimeTicks] = i
		seenOrdinals[e.FrameOrdinal] = i
		if chk.OK {
			switch {
			case e.Offset < 0 || uint64(e.Offset) >= uint64(res.StreamBytes):
				chk.OK, chk.Code = false, fault.CodeIndexOutOfRange
				chk.Detail = fmt.Sprintf("offset %d outside stream of %d bytes", e.Offset, res.StreamBytes)
			default:
				if _, isRAP := rapByOffset[e.Offset]; !isRAP {
					chk.OK, chk.Code = false, fault.CodeIndexNotKeyframe
					chk.Detail = fmt.Sprintf("offset %d does not point at a random access point", e.Offset)
				}
			}
		}
		if !chk.OK {
			report.Rejected++
			errs = append(errs, fault.At(fault.StageScan, chk.Code, res.Codec, e.Offset, chk.Detail))
		} else {
			report.Accepted++
		}
		report.Checks = append(report.Checks, chk)
	}
	return report, fault.Aggregate(errs)
}

// BuildKeyIndex derives the canonical index for a clean scan, using one
// source clock tick per frame slot starting at zero.
func BuildKeyIndex(res *ScanResult, timeDen uint64) *KeyIndex {
	idx := &KeyIndex{TimeDen: timeDen}
	for _, kfi := range res.Keyframes {
		au := res.AccessUnits[kfi]
		idx.Entries = append(idx.Entries, IndexEntry{
			Offset:       au.Offset,
			TimeTicks:    uint64(kfi),
			FrameOrdinal: kfi,
		})
	}
	return idx
}
