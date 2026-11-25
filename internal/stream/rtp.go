package stream

import (
	"bufio"
	"bytes"
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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := readAllBounded(f, 1<<30)
	if err != nil {
		return nil, err
	}
	sess := &RTPSession{Input: path, ClockRate: defaultClockRate}
	if len(data) < len(rtpMagic) || string(data[:len(rtpMagic)]) != rtpMagic {
		sess.Issues.Add(issue.New(issue.UnsupportedInput, path,
			"not an MSRTP1 recording").WithByte(0))
		return sess, nil
	}
	off := int64(len(rtpMagic))
	if len(data) < int(off)+4 {
		sess.Issues.Add(issue.New(issue.RTPCorrupt, path, "missing clock rate field").WithByte(off))
		sess.Damaged = append(sess.Damaged, issue.ByteRange{Input: path, Start: off, End: int64(len(data)),
			Categories: []issue.Category{issue.RTPCorrupt}})
		return sess, nil
	}
	sess.ClockRate = binary.BigEndian.Uint32(data[off : off+4])
	off += 4
	unw := clock.NewUnwrapper(1 << 32)
	rate := sess.ClockRate
	for off < int64(len(data)) {
		if int64(len(data))-off < 4 {
			sess.damaged(path, off, int64(len(data)), issue.RTPCorrupt, "truncated length prefix")
			break
		}
		length := int64(binary.BigEndian.Uint32(data[off : off+4]))
		recStart := off
		hdrStart := off + 4
		if length <= 0 || hdrStart+length > int64(len(data)) {
			sess.damaged(path, recStart, int64(len(data)), issue.RTPCorrupt,
				fmt.Sprintf("packet record declares %d bytes, %d available", length, int64(len(data))-hdrStart))
			break
		}
		buf := data[hdrStart : hdrStart+length]
		idx := len(sess.Packets) + 1
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			sess.damaged(path, hdrStart, hdrStart+length, issue.RTPCorrupt,
				fmt.Sprintf("packet %d: %v", idx, err))
			off = hdrStart + length
			continue
		}
		if pkt.Version != 2 {
			sess.damaged(path, hdrStart, hdrStart+length, issue.RTPCorrupt,
				fmt.Sprintf("packet %d: RTP version %d != 2", idx, pkt.Version))
			off = hdrStart + length
			continue
		}
		u := unw.Push(uint64(pkt.Timestamp))
		if u.Duplicate {
			sess.Duplicates++
			sess.Issues.Add(issue.New(issue.TimestampDuplicate, path,
				fmt.Sprintf("RTP timestamp %d repeated at packet %d", pkt.Timestamp, idx)).
				WithByte(hdrStart).WithPacket(idx))
		}
		if u.Regressed {
			sess.Regressions++
			sess.Issues.Add(issue.New(issue.TimestampRegress, path,
				fmt.Sprintf("RTP timestamp %d regressed at packet %d", pkt.Timestamp, idx)).
				WithByte(hdrStart).WithPacket(idx))
		}
		if u.Missing > 0 {
			sess.Missing += u.Missing
			sess.Issues.Add(issue.New(issue.TimestampMissing, path,
				fmt.Sprintf("%d timestamps missing before packet %d", u.Missing, idx)).
				WithByte(hdrStart).WithPacket(idx).WithRef(fmt.Sprintf("missing:%d", u.Missing)))
		}
		tick := clock.Tick(0)
		if rate > 0 {
			tick = clock.Tick((u.Value*90000 + uint64(rate)/2) / uint64(rate))
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

func (s *RTPSession) damaged(input string, start, end int64, cat issue.Category, detail string) {
	s.Issues.Add(issue.New(cat, input, detail).WithByte(start))
	s.Damaged = append(s.Damaged, issue.ByteRange{Input: input, Start: start, End: end,
		Categories: []issue.Category{cat}})
}

func readAllBounded(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	br := bufio.NewReaderSize(r, 64*1024)
	_, err := io.Copy(&buf, io.LimitReader(br, limit))
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
