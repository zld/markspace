package stream

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// RTPPacket is one parsed RTP datagram and its position in the file.
type RTPPacket struct {
	Seq         uint16
	TS          uint32
	Expanded    uint64
	Offset      int64 // byte offset of the packet record in the input
	Length      int
	PayloadLen  int
	Marker      bool
	PayloadType uint8
	SSRC        uint32
	Index       int
	Arrival     clock.Tick // expanded timestamp mapped onto the shared base
}

// RTPSession is the analysis of one RTP recording.
type RTPSession struct {
	Input       string
	ClockRate   uint32
	Packets     []RTPPacket
	Wraps       int
	Duplicates  int
	Regressions int
	Missing     uint64
	Damaged     []issue.ByteRange
	Issues      issue.List
}

// rtpFileHeader is the synthetic fixture container written by tests/demo:
//
//	magic "MSRTP1\n" | clockRate uint32 BE | repeat of (length uint32 BE | packet)
//
// A packet length that is impossible or a truncated record is the damaged
// byte location.
const rtpMagic = "MSRTP1\n"

// ParseRTPFile reads the synthetic recording format. Unknown magic is a
// hard unsupported-input error, not a guess.
func ParseRTPFile(path string, defaultClockRate uint32) (*RTPSession, error) {
	return ParseRTPFileAs(path, path, defaultClockRate)
}

// ParseRTPFileAs is ParseRTPFile with a stable label for reports. The file is
// read record by record, so memory stays proportional to the parsed packet
// list rather than the input size.
func ParseRTPFileAs(path, label string, defaultClockRate uint32) (*RTPSession, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	total := stat.Size()
	br := bufio.NewReaderSize(f, 64*1024)
	sess := &RTPSession{Input: label, ClockRate: defaultClockRate}
	hdr := make([]byte, len(rtpMagic))
	if _, err := io.ReadFull(br, hdr); err != nil || string(hdr) != rtpMagic {
		sess.Issues.Add(issue.New(issue.UnsupportedInput, label,
			"not an MSRTP1 recording").WithByte(0))
		return sess, nil
	}
	off := int64(len(rtpMagic))
	var rateBuf [4]byte
	if _, err := io.ReadFull(br, rateBuf[:]); err != nil {
		sess.Issues.Add(issue.New(issue.RTPCorrupt, label, "missing clock rate field").WithByte(off))
		sess.Damaged = append(sess.Damaged, issue.ByteRange{Input: label, Start: off, End: total,
			Categories: []issue.Category{issue.RTPCorrupt}})
		return sess, nil
	}
	sess.ClockRate = binary.BigEndian.Uint32(rateBuf[:])
	off += 4

	unw := clock.NewUnwrapper(1 << 32)
	rate := sess.ClockRate
	var prevSeq uint16
	haveSeq := false
	var base uint64
	haveBase := false
	buf := make([]byte, 0, 1500)
	for off < total {
		if total-off < 4 {
			sess.damaged(label, off, total, issue.RTPCorrupt, "truncated length prefix")
			break
		}
		var lenBuf [4]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			sess.damaged(label, off, total, issue.RTPCorrupt, "truncated length prefix")
			break
		}
		length := int64(binary.BigEndian.Uint32(lenBuf[:]))
		recStart := off
		hdrStart := off + 4
		if length <= 0 || hdrStart+length > total {
			sess.damaged(label, recStart, total, issue.RTPCorrupt,
				fmt.Sprintf("packet record declares %d bytes, %d available", length, total-hdrStart))
			break
		}
		if int64(cap(buf)) < length {
			buf = make([]byte, length)
		}
		payload := buf[:length]
		if _, err := io.ReadFull(br, payload); err != nil {
			sess.damaged(label, recStart, total, issue.RTPCorrupt, "truncated packet record")
			break
		}
		idx := len(sess.Packets) + 1
		var pkt rtp.Packet
		if err := pkt.Unmarshal(payload); err != nil {
			sess.damaged(label, hdrStart, hdrStart+length, issue.RTPCorrupt,
				fmt.Sprintf("packet %d: %v", idx, err))
			off = hdrStart + length
			continue
		}
		if pkt.Version != 2 {
			sess.damaged(label, hdrStart, hdrStart+length, issue.RTPCorrupt,
				fmt.Sprintf("packet %d: RTP version %d != 2", idx, pkt.Version))
			off = hdrStart + length
			continue
		}
		// Missing packets are detected from the sequence number: timestamps
		// normally advance per frame even when no frame is missing.
		if haveSeq {
			gap := seqGap(prevSeq, pkt.SequenceNumber)
			if gap > 1 {
				sess.Missing += uint64(gap) - 1
				sess.Issues.Add(issue.New(issue.TimestampMissing, label,
					fmt.Sprintf("%d packet(s) missing before sequence %d", gap-1, pkt.SequenceNumber)).
					WithByte(hdrStart).WithPacket(idx).WithRef(fmt.Sprintf("missing:%d", gap-1)))
			}
		}
		prevSeq, haveSeq = pkt.SequenceNumber, true

		u := unw.Push(uint64(pkt.Timestamp))
		if u.Duplicate {
			sess.Duplicates++
			sess.Issues.Add(issue.New(issue.TimestampDuplicate, label,
				fmt.Sprintf("RTP timestamp %d repeated at packet %d", pkt.Timestamp, idx)).
				WithByte(hdrStart).WithPacket(idx))
		}
		if u.Regressed {
			sess.Regressions++
			sess.Issues.Add(issue.New(issue.TimestampRegress, label,
				fmt.Sprintf("RTP timestamp %d regressed at packet %d", pkt.Timestamp, idx)).
				WithByte(hdrStart).WithPacket(idx))
		}
		// The timeline is anchored at the first packet so a stream that
		// starts near the 32-bit wrap still begins at t=0. Expansion keeps
		// later packets ordered across the boundary.
		if !haveBase {
			base = u.Value
			haveBase = true
		}
		tick := clock.Tick(0)
		if rate > 0 {
			tick = clock.Tick(((u.Value-base)*90000 + uint64(rate)/2) / uint64(rate))
		}
		sess.Packets = append(sess.Packets, RTPPacket{
			Seq: pkt.SequenceNumber, TS: pkt.Timestamp, Expanded: u.Value,
			Offset: hdrStart, Length: int(length), PayloadLen: len(pkt.Payload),
			Marker: pkt.Marker, PayloadType: pkt.PayloadType, SSRC: pkt.SSRC,
			Index: idx, Arrival: tick,
		})
		off = hdrStart + length
	}
	sess.Wraps = unw.Wraps()
	sess.Damaged = issue.MergeRanges(sess.Damaged, 4)
	return sess, nil
}

func seqGap(prev, cur uint16) int {
	if cur >= prev {
		return int(cur) - int(prev)
	}
	return 0x10000 - int(prev) + int(cur)
}

// EncodeRTPFile writes the synthetic RTP container used by fixtures.
func EncodeRTPFile(w io.Writer, clockRate uint32, packets [][]byte) error {
	if _, err := io.WriteString(w, rtpMagic); err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], clockRate)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	for _, p := range packets {
		binary.BigEndian.PutUint32(hdr[:], uint32(len(p)))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

// MarshalPacket builds a valid RTP packet for fixtures.
func MarshalPacket(seq uint16, ts uint32, marker bool, pt uint8, ssrc uint32, payload []byte) ([]byte, error) {
	p := rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: marker, PayloadType: pt,
			SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
		},
		Payload: payload,
	}
	return p.Marshal()
}

func sortedKeys(m map[issue.Category]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

func (s *RTPSession) damaged(input string, start, end int64, cat issue.Category, detail string) {
	s.Issues.Add(issue.New(cat, input, detail).WithByte(start))
	s.Damaged = append(s.Damaged, issue.ByteRange{Input: input, Start: start, End: end,
		Categories: []issue.Category{cat}})
}
