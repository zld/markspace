// Package rtpcheck validates offline RTP captures that accompany a delivery
// master: sequence continuity per SSRC, timestamp progression against the
// declared clock rate, and interleaving against the media fragment order.
//
// The parser reads a compact text dump, one packet per line:
//
//	<ssrc> <seq> <timestamp> <clock_rate>
//
// Each line is encoded with pion/rtp's packet marshaller so the validation runs
// against the same wire format a receiver would see, not a private struct.
package rtpcheck

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pion/rtp"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
)

// Record is one parsed capture line keyed to a media track.
type Record struct {
	Track uint64
	Seq   uint16
	TS    uint32
	Rate  uint32
	SSRC  uint32
	Line  int
	// Pion is the packet decoded back through pion/rtp.
	Pion rtp.Packet
}

// ParseDump reads an RTP capture dump. Track numbers are 1-based and assigned in
// first-seen SSRC order so a dump without media fragments still validates.
func ParseDump(path string) ([]Record, map[uint32]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"open rtp dump %s", path)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 32<<10)
	ssrcTrack := map[uint32]uint64{}
	next := uint64(1)
	var out []Record
	for line := 1; ; line++ {
		s, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
				"read rtp dump line %d", line)
		}
		txt := strings.TrimSpace(s)
		if txt != "" && !strings.HasPrefix(txt, "#") {
			rec, err := parseLine(txt, line, ssrcTrack, &next)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, rec)
		}
		if err == io.EOF {
			break
		}
	}
	return out, ssrcTrack, nil
}

func parseLine(txt string, line int, ssrcTrack map[uint32]uint64, next *uint64) (Record, error) {
	fields := strings.Fields(txt)
	if len(fields) != 4 {
		return Record{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, 0, -1,
			"rtp dump line %d: want 4 fields, got %d", line, len(fields))
	}
	vals := make([]uint64, 4)
	for i, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return Record{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, 0, -1,
				"rtp dump line %d field %d: %v", line, i+1, err)
		}
		vals[i] = v
	}
	ssrc, seq, ts, rate := uint32(vals[0]), uint16(vals[1]), uint32(vals[2]), uint32(vals[3])
	if rate == 0 {
		return Record{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, 0, -1,
			"rtp dump line %d: clock rate is zero", line)
	}
	if vals[1] > 0xFFFF {
		return Record{}, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, 0, -1,
			"rtp dump line %d: sequence %s exceeds 16 bits", line, fields[1])
	}
	if vals[2] > 0xFFFFFFFF {
		return Record{}, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, 0, -1,
			"rtp dump line %d: timestamp %s exceeds 32 bits", line, fields[2])
	}
	if _, ok := ssrcTrack[ssrc]; !ok {
		ssrcTrack[ssrc] = *next
		*next++
	}
	pkt := &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
	}, Payload: []byte{0x00}}
	raw, err := pkt.Marshal()
	if err != nil {
		return Record{}, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"rtp dump line %d: marshal packet", line)
	}
	var decoded rtp.Packet
	if err := decoded.Unmarshal(raw); err != nil {
		return Record{}, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"rtp dump line %d: decode packet", line)
	}
	return Record{
		Track: ssrcTrack[ssrc], Seq: decoded.SequenceNumber, TS: decoded.Timestamp,
		Rate: rate, SSRC: decoded.SSRC, Line: line, Pion: decoded,
	}, nil
}

// Problem is one RTP-level failure at a dump line.
type Problem struct {
	Category timeline.Category
	Track    uint64
	Line     int
	Reason   string
}

// Report summarises RTP validation.
type Report struct {
	Packets  int
	Streams  int
	MaxSkew  timeline.Nanosecond
	Problems []Problem
}

func (r Report) OK() bool { return len(r.Problems) == 0 }

// Validate checks each stream for sequence continuity and timestamp monotonicity
// and, when media fragments are supplied, that the first RTP timestamp of each
// track aligns with that track's first fragment within 1 ms.
func Validate(recs []Record, a *media.Asset) Report {
	rep := Report{Packets: len(recs)}
	byTrack := map[uint64][]Record{}
	for _, r := range recs {
		byTrack[r.Track] = append(byTrack[r.Track], r)
	}
	rep.Streams = len(byTrack)
	order := make([]uint64, 0, len(byTrack))
	for t := range byTrack {
		order = append(order, t)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	for _, tn := range order {
		rs := byTrack[tn]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Line < rs[j].Line })
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			delta := int16(cur.Seq - prev.Seq)
			switch {
			case delta == 0:
				rep.Problems = append(rep.Problems, Problem{
					Category: timeline.CatDuplicate, Track: tn, Line: cur.Line,
					Reason: fmt.Sprintf("sequence %d repeated", cur.Seq),
				})
			case delta < 0:
				rep.Problems = append(rep.Problems, Problem{
					Category: timeline.CatOrder, Track: tn, Line: cur.Line,
					Reason: fmt.Sprintf("sequence went backwards: %d after %d", cur.Seq, prev.Seq),
				})
			case delta != 1:
				rep.Problems = append(rep.Problems, Problem{
					Category: timeline.CatMissingElement, Track: tn, Line: cur.Line,
					Reason: fmt.Sprintf("sequence gap: %d follows %d", cur.Seq, prev.Seq),
				})
			}
			prevTS := timestampNanos(prev)
			curTS := timestampNanos(cur)
			if curTS < prevTS {
				rep.Problems = append(rep.Problems, Problem{
					Category: timeline.CatOrder, Track: tn, Line: cur.Line,
					Reason: fmt.Sprintf("timestamp went backwards: %d after %d", cur.TS, prev.TS),
				})
			}
		}
	}
	rep.MaxSkew = alignToFragments(recs, a, &rep)
	return rep
}

func timestampNanos(r Record) timeline.Nanosecond {
	// RTP timestamps wrap at 32 bits; compare within one wrap window.
	return timeline.Nanosecond(int64(r.TS)) * timeline.Nanosecond(1_000_000_000) /
		timeline.Nanosecond(r.Rate)
}

// alignToFragments checks that each track's first RTP timestamp, mapped to the
// media base, matches the track's first fragment within tolerance.
func alignToFragments(recs []Record, a *media.Asset, rep *Report) timeline.Nanosecond {
	if a == nil || len(a.Fragments) == 0 {
		return 0
	}
	firstFrag := map[uint64]timeline.Nanosecond{}
	for _, f := range a.Fragments {
		if prev, ok := firstFrag[f.TrackNumber]; !ok || f.Start < prev {
			firstFrag[f.TrackNumber] = f.Start
		}
	}
	firstRec := map[uint64]Record{}
	for _, r := range recs {
		if prev, ok := firstRec[r.Track]; !ok || r.Line < prev.Line {
			firstRec[r.Track] = r
		}
	}
	var maxSkew timeline.Nanosecond
	for tn, rec := range firstRec {
		start, ok := firstFrag[tn]
		if !ok {
			continue
		}
		skew := timeline.DeltaNS(timestampNanos(rec), start)
		if skew > maxSkew {
			maxSkew = skew
		}
		if skew > timeline.MaxTolerance {
			rep.Problems = append(rep.Problems, Problem{
				Category: timeline.CatFragmentConflict, Track: tn, Line: rec.Line,
				Reason: fmt.Sprintf("rtp start %s ns differs from fragment start %s ns by %d ns",
					timeline.FormatNS(timestampNanos(rec)), timeline.FormatNS(start), skew),
			})
		}
	}
	return maxSkew
}
