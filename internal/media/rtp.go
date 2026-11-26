package media

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"
)

// rtpMagic prefixes every RTP capture consumed by this kernel.
var rtpMagic = [4]byte{'R', 'T', 'P', '1'}

// RTPHeader is the fixed 16-byte container header (magic + clock rate).
type RTPHeader struct {
	Magic     [4]byte
	ClockRate uint32 // RTP timestamp ticks per second
	Reserved  uint32 // must be zero; reserved for future flags
}

// WriteRTPCapture serializes RTP packets into the kernel's capture format.
// It is shared by the fixture generator and keeps framing deterministic.
func WriteRTPCapture(w io.Writer, clockRate uint32, packets []*rtp.Packet) error {
	if clockRate == 0 {
		return NewError(StageScan, "rtp.header", "zero RTP clock rate")
	}
	hdr := RTPHeader{Magic: rtpMagic, ClockRate: clockRate}
	if err := binary.Write(w, binary.BigEndian, &hdr); err != nil {
		return err
	}
	for _, p := range packets {
		raw, err := p.Marshal()
		if err != nil {
			return NewError(StageScan, "rtp.marshal", err.Error())
		}
		if len(raw) > 0x7fffffff {
			return NewError(StageScan, "rtp.frame", "packet larger than int32 length")
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		if _, err := w.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	return nil
}

// rtpFrame is one parsed packet plus its container byte offset.
type rtpFrame struct {
	pkt   rtp.Packet
	tick  int64 // cumulative unwrapped timestamp in clock ticks
	delta int64 // modular delta from the previous packet, 0 for the first
	off   int64 // byte offset of the frame length prefix
}

// ParseRTP reads an RTP capture and reconstructs sample timing. Timestamp
// wrap is resolved on the 32-bit modular clock; samples are then placed on a
// monotonically increasing unwrapped tick counter before nanosecond
// conversion, so no floating point ever accumulates.
func ParseRTP(path, trackKey string) (*Track, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, NewError(StageScan, "rtp.open", err.Error())
	}
	defer f.Close()

	off, hdr, err := readRTPHeader(f)
	if err != nil {
		return nil, err
	}
	tb, err := NewTimeBase(int64(hdr.ClockRate))
	if err != nil {
		return nil, NewError(StageScan, "rtp.header", err.Error()).WithTrack(trackKey)
	}

	br := bufio.NewReader(f)
	frames := make([]rtpFrame, 0, 16)
	var (
		prevRaw  uint32
		cumTick  int64
		lastStep int64
	)
	for {
		frameOff := off
		var lenBuf [4]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, NewError(StageScan, "rtp.frame", "truncated length prefix: "+err.Error()).
				WithTrack(trackKey).WithOffset(frameOff)
		}
		off += 4
		frameLen := binary.BigEndian.Uint32(lenBuf[:])
		raw := make([]byte, frameLen)
		if _, err := io.ReadFull(br, raw); err != nil {
			return nil, NewError(StageScan, "rtp.frame", "truncated packet body: "+err.Error()).
				WithTrack(trackKey).WithOffset(off)
		}
		off += int64(frameLen)

		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, NewError(StageScan, "rtp.packet", err.Error()).
				WithTrack(trackKey).WithOffset(frameOff)
		}
		var step int64
		if len(frames) > 0 {
			step = int64(WrapDelta32(prevRaw, pkt.Timestamp))
			if step == 0 {
				return nil, NewError(StageScan, "rtp.time", "zero-duration sample from equal timestamps").
					WithTrack(trackKey).WithOffset(frameOff)
			}
			cumTick += step
			lastStep = step
		}
		frames = append(frames, rtpFrame{pkt: pkt, tick: cumTick, delta: step, off: frameOff})
		prevRaw = pkt.Timestamp
	}
	if len(frames) == 0 {
		return nil, NewError(StageScan, "rtp.frame", "capture contains no packets").WithTrack(trackKey)
	}
	if lastStep == 0 {
		lastStep = 1 // single-packet capture: nominal one-tick duration
	}

	track := &Track{Key: trackKey, TimeBase: tb}
	for i, fr := range frames {
		startNs, err := tb.TicksToNs(fr.tick)
		if err != nil {
			return nil, NewError(StageScan, "rtp.time", err.Error()).
				WithTrack(trackKey).WithOffset(fr.off)
		}
		durTicks := fr.delta
		if i == len(frames)-1 {
			durTicks = lastStep
		}
		durNs, err := tb.DeltaNs(durTicks)
		if err != nil {
			return nil, NewError(StageScan, "rtp.time", err.Error()).
				WithTrack(trackKey).WithOffset(fr.off)
		}
		if err := track.Add(startNs, durNs, fr.off, int64(len(fr.pkt.Payload)), fr.tick,
			append([]byte(nil), fr.pkt.Payload...)); err != nil {
			return nil, err
		}
	}
	return track, nil
}

func readRTPHeader(r io.Reader) (int64, RTPHeader, error) {
	var hdr RTPHeader
	if err := binary.Read(r, binary.BigEndian, &hdr); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, hdr, NewError(StageScan, "rtp.header", "truncated capture header")
		}
		return 0, hdr, NewError(StageScan, "rtp.header", err.Error())
	}
	if hdr.Magic != rtpMagic {
		return 0, hdr, NewError(StageScan, "rtp.header",
			fmt.Sprintf("bad magic %q, want RTP1", hdr.Magic[:]))
	}
	if hdr.ClockRate == 0 {
		return 16, hdr, NewError(StageScan, "rtp.header", "zero RTP clock rate").WithOffset(16)
	}
	if hdr.Reserved != 0 {
		return 16, hdr, NewError(StageScan, "rtp.header", "non-zero reserved header field").WithOffset(12)
	}
	return 16, hdr, nil
}
