package container

import (
	"fmt"
	"os"

	"mediacore/internal/core"
	"mediacore/internal/timeline"
)

// rescan rebuilds a track index from verifiable sample records after moov is
// missing or its tables are gone. It only accepts regions it can prove: a
// magic header, a sequence number in order, a payload length that fits, and a
// matching CRC. Anything else becomes a gap.
func rescan(f *os.File, fileSize int64, out *TrackRecovery, lim Limits, scan *Scan, tr *TrackScan) *core.Error {
	rate := int64(0)
	channels := 0
	if tr != nil && tr.Format.SampleRate > 0 {
		rate = int64(tr.Format.SampleRate)
	}
	if tr != nil {
		channels = tr.Format.Channels
	}

	dataStart, dataEnd, ok := mediaBounds(scan)
	if !ok {
		return core.New(core.CodeUnrecoverable, core.StageRecover, false,
			"no mdat payload to rescan; index cannot be rebuilt")
	}
	if dataEnd > fileSize {
		dataEnd = fileSize
	}
	if dataStart >= dataEnd {
		return core.New(core.CodeUnrecoverable, core.StageRecover, false, "mdat is empty")
	}

	insp := RecordInspector{Limit: lim.MaxPayloadSample}
	off := dataStart
	seq := uint32(0)
	ts := timeline.Ticks(0)
	sampleIdx := 0
	for off < dataEnd {
		remaining := dataEnd - off
		if remaining < fixtureRecordHeader {
			out.Warnings = append(out.Warnings, core.New(core.CodeBoxTruncated, core.StageRecover, true,
				fmt.Sprintf("%d trailing bytes cannot hold a record header", remaining)).WithOffset(off))
			break
		}
		length, frames, ch, err := insp.Inspect(f, off, dataEnd, seq)
		if err != nil {
			// Record boundary lost: locate the next magic and mark the bytes
			// between as an unrecoverable gap.
			next := findMagic(f, off, dataEnd)
			out.Warnings = append(out.Warnings, core.Wrap(core.CodeCorruptFragment, core.StageRecover, true,
				fmt.Sprintf("record %d rejected: %v", seq, err), err).WithOffset(off))
			out.Samples = append(out.Samples, Sample{
				Index: sampleIdx, Offset: off, Size: max64(next-off, 0),
				Start: ts, Duration: timeline.Ticks(1024), Reason: "unverifiable fragment",
				Err: core.New(core.CodeCorruptFragment, core.StageRecover, true, "unverifiable fragment").WithOffset(off),
			})
			sampleIdx++
			ts += 1024
			if next <= off {
				break
			}
			off = next
			continue
		}
		if rate == 0 {
			rate = int64(recordRate(f, off)) // record header carries the true rate
			if rate == 0 {
				rate = 1 // unknown rate: still count frames in a 1 Hz grid
			}
		}
		if channels == 0 {
			channels = ch
		}
		dur := timeline.Ticks(frames)
		out.Samples = append(out.Samples, Sample{
			Index: sampleIdx, Offset: off, Size: length, Start: ts, Duration: dur, Decoded: true,
		})
		ts += dur
		off += length
		seq++
		sampleIdx++
	}
	if seq == 0 {
		return core.New(core.CodeUnrecoverable, core.StageRecover, false,
			"no verifiable records found in mdat").WithOffset(dataStart)
	}
	out.Timescale = timeline.MustScale(rate)
	if out.Format.SampleRate == 0 {
		out.SourceFormat = AudioFormat{Codec: "pcm-record", SampleRate: int(rate), Channels: channels}
	}
	out.TotalBytes = dataEnd - dataStart
	return nil
}

// mediaBounds returns the first mdat payload range recorded by the scanner.
func mediaBounds(s *Scan) (start, end int64, ok bool) {
	if s == nil || len(s.Mdat) == 0 {
		return 0, 0, false
	}
	m := s.Mdat[0]
	start = m.PayloadOffset()
	end = m.Offset + m.Size
	if end <= start {
		return 0, 0, false
	}
	return start, end, true
}

// findMagic scans forward for the next record magic. It reads in bounded
// chunks so a corrupt file cannot force a large allocation.
func findMagic(f *os.File, from, end int64) int64 {
	buf := make([]byte, 64<<10)
	pos := from + 1
	for pos < end {
		n := int64(len(buf))
		if end-pos < n {
			n = end - pos
		}
		got, err := f.ReadAt(buf[:n], pos)
		if got <= 0 || err != nil && got == 0 {
			return 0
		}
		for i := 0; i+1 < got; i++ {
			if buf[i] == 0xAF && buf[i+1] == 0x01 {
				return pos + int64(i)
			}
		}
		pos += int64(got)
	}
	return 0
}

// recordRate reads the sample rate field of an already-verified record.
func recordRate(f *os.File, off int64) uint32 {
	var b [4]byte
	if _, err := f.ReadAt(b[:], off+4); err != nil {
		return 0
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
