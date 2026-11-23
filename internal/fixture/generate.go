// Package fixture generates deterministic RTP captures for the mediacheck
// demo: a normal stream, a reordered/duplicated stream, a truncated and
// corrupt stream, and a stream with timestamp wrap and late packets.
package fixture

import (
	"math"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediaforge/internal/mediaerr"
	"mediaforge/internal/packet"
)

// Stream parameters shared by all four fixtures.
const (
	Rate        = 48000
	Channels    = 2
	BitsPerSam  = 16
	FrameTicks  = 960 // 20 ms at 48 kHz
	TotalFrames = 250 // 5 seconds
	SSRC        = 0x4D465031
	PayloadType = 96
)

// Names of the four generated fixtures.
const (
	Normal    = "normal.rtp"
	Reordered = "reordered.rtp"
	Damaged   = "damaged.rtp"
	Wrap      = "wrap.rtp"
)

// All returns the fixture file names in demo order.
func All() []string { return []string{Normal, Reordered, Damaged, Wrap} }

// tone renders one packet of interleaved 16-bit PCM at a fixed level.
func tone(frame int) []byte {
	samples := FrameTicks * Channels
	buf := make([]byte, samples*2)
	for i := 0; i < FrameTicks; i++ {
		phase := 2 * math.Pi * 997 * float64(frame*FrameTicks+i) / Rate
		v := int16(math.Round(0.25 * 32767 * math.Sin(phase)))
		for c := 0; c < Channels; c++ {
			off := (i*Channels + c) * 2
			buf[off] = byte(uint16(v))
			buf[off+1] = byte(uint16(v) >> 8)
		}
	}
	return buf
}

func makePacket(seq uint16, ts uint32, payload []byte, crc bool) ([]byte, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: PayloadType,
			SequenceNumber: seq, Timestamp: ts, SSRC: SSRC,
			Marker: seq%10 == 9,
		},
		Payload: payload,
	}
	if crc {
		if err := p.SetExtension(packet.ExtensionCRC, []byte{0, 0, 0, 0}); err != nil {
			return nil, err
		}
		c := packet.FrameCRC(payload)
		if err := p.SetExtension(packet.ExtensionCRC, []byte{
			byte(c >> 24), byte(c >> 16), byte(c >> 8), byte(c)}); err != nil {
			return nil, err
		}
	}
	return p.Marshal()
}

// createFile is a thin wrapper so tests can substitute the file sink.
var createFile = func(name string) (*os.File, error) { return os.Create(name) }

// Generate writes all four fixtures into dir.
func Generate(dir string) error {
	writers := []func(string) error{genNormal, genReordered, genDamaged, genWrap}
	for _, w := range writers {
		if err := w(dir); err != nil {
			return err
		}
	}
	return nil
}

func newCapture(dir, name string) (*packet.Writer, func() error, error) {
	path := filepath.Join(dir, name)
	f, err := createFile(path)
	if err != nil {
		return nil, nil, err
	}
	w, err := packet.NewWriter(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return w, f.Close, nil
}

func genNormal(dir string) error {
	w, closeFn, err := newCapture(dir, Normal)
	if err != nil {
		return err
	}
	for i := 0; i < TotalFrames; i++ {
		raw, err := makePacket(uint16(i), uint32(i*FrameTicks), tone(i), true)
		if err != nil {
			closeFn()
			return mediaerr.New(mediaerr.CodeIO, "build normal packet: %v", err)
		}
		if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
			closeFn()
			return err
		}
	}
	return closeFn()
}

// genReordered writes the same audio with adjacent packets swapped and
// duplicates inserted; statistics must match the normal stream.
func genReordered(dir string) error {
	w, closeFn, err := newCapture(dir, Reordered)
	if err != nil {
		return err
	}
	defer closeFn()
	order := make([]int, 0, TotalFrames+8)
	for i := 0; i < TotalFrames; i++ {
		if i%7 == 3 && i+1 < TotalFrames {
			order = append(order, i+1, i)
			i++
			continue
		}
		order = append(order, i)
	}
	for _, i := range order {
		raw, err := makePacket(uint16(i), uint32(i*FrameTicks), tone(i), true)
		if err != nil {
			return err
		}
		if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
			return err
		}
	}
	// Duplicate a few already-written packets.
	for _, i := range []int{0, 40, 120} {
		raw, err := makePacket(uint16(i), uint32(i*FrameTicks), tone(i), true)
		if err != nil {
			return err
		}
		if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
			return err
		}
	}
	return nil
}

// genDamaged drops a run of packets, corrupts one payload, writes one packet
// with a bad version and truncates the final record.
func genDamaged(dir string) error {
	path := filepath.Join(dir, Damaged)
	f, err := createFile(path)
	if err != nil {
		return err
	}
	w, err := packet.NewWriter(f)
	if err != nil {
		f.Close()
		return err
	}
	if err := writeDamaged(w); err != nil {
		f.Close()
		return err
	}
	// Append a record header that promises more bytes than follow, which the
	// reader must report as a truncated tail while keeping earlier audio.
	if _, err := f.Write(append([]byte{0, 0, 2, 0, byte(packet.KindRTP)}, make([]byte, 40)...)); err != nil {
		f.Close()
		return mediaerr.New(mediaerr.CodeIO, "truncate damaged: %v", err)
	}
	return f.Close()
}

func writeDamaged(w *packet.Writer) error {
	for i := 0; i < TotalFrames; i++ {
		if i >= 90 && i < 96 {
			continue // six-packet hole: 120 ms missing
		}
		payload := tone(i)
		raw, err := makePacket(uint16(i), uint32(i*FrameTicks), payload, true)
		if err != nil {
			return err
		}
		if i == 150 {
			// Flip a payload byte after the CRC was computed so the packet
			// still parses structurally but fails its integrity check.
			raw[len(raw)-100] ^= 0xFF
		}
		if i == 60 {
			raw[0] = (raw[0] &^ 0xC0) | (3 << 6) // bad RTP version
		}
		if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
			return err
		}
	}
	return nil
}

// genWrap starts timestamps just below the 32-bit wrap point and delivers
// three packets after the watermark has already advanced past them.
func genWrap(dir string) error {
	w, closeFn, err := newCapture(dir, Wrap)
	if err != nil {
		return err
	}
	defer closeFn()
	var base uint32 = 0xFFFFF000
	late := map[int]bool{15: true, 16: true, 22: true}
	var held []int
	for i := 0; i < 120; i++ {
		ts := base + uint32(i*FrameTicks)
		if late[i] {
			held = append(held, i)
			continue
		}
		raw, err := makePacket(uint16(i), ts, tone(i), true)
		if err != nil {
			return err
		}
		if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
			return err
		}
		if i == 30 {
			for _, j := range held {
				raw, err := makePacket(uint16(j), base+uint32(j*FrameTicks), tone(j), true)
				if err != nil {
					return err
				}
				if err := w.WriteRecord(packet.KindRTP, raw); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
