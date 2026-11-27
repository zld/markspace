package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"regress/internal/clock"
	"regress/internal/model"
)

// RTP payload types assigned inside the generated capture.
const (
	PTVideoHigh = 96
	PTVideoLow  = 97
	PTAudio     = 98
	PTMeta      = 99
)

// SSRCs per logical stream.
const (
	SSRCHigh uint32 = 0xA1100001
	SSRCLow  uint32 = 0xA1100002
	SSRCAud  uint32 = 0xA1100003
	SSRCMet  uint32 = 0xA1100004
)

// Meta payload wire tags (first payload byte).
const (
	MetaTagID3      byte = 1
	MetaTagEmsg     byte = 2
	MetaTagSEI      byte = 3
	MetaTagSubtitle byte = 4
	MetaTagChapter  byte = 5
)

// Capture is the serialized RTP capture: a small big-endian record header
// followed by one length-prefixed RTP packet per record.
type Capture struct {
	Packets []*rtp.Packet
	// TruncatedByte is non-zero for the truncate fault and points at the
	// record boundary where the capture must be cut on disk.
	TruncatedByte int64
}

var metaTag = map[string]byte{
	MetaID3:  MetaTagID3,
	MetaEmsg: MetaTagEmsg,
	MetaSEI:  MetaTagSEI,
	MetaSub:  MetaTagSubtitle,
	MetaChap: MetaTagChapter,
}

// BuildRTP renders the transport capture. Timestamps start close to the 32-bit
// wrap boundary so FaultWrap (and even the normal scenario) exercises modular
// expansion; FaultWrap forces an actual wrap within the first few packets.
func BuildRTP(s Spec, plan []model.PlanItem) (*Capture, error) {
	cap := &Capture{}
	// Start 8 video-frame intervals below the 2^32 boundary. Under FaultWrap
	// every timestamp is taken modulo 2^32 regardless, so a real wrap occurs.
	var base uint32 = 0xFFFFFFFF - uint32(8*VideoFrameTick) + 1
	seq16 := uint16(0)

	emit := func(pt uint8, ssrc uint32, ts32 uint32, payload []byte) {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    pt,
				SequenceNumber: seq16,
				Timestamp:      ts32,
				SSRC:           ssrc,
				Marker:         pt == PTMeta,
			},
			Payload: payload,
		}
		seq16++
		cap.Packets = append(cap.Packets, pkt)
	}

	// Video renditions: 90 kHz RTP clock, one packet per frame.
	for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
		abs := int64(base) + int64(seq)*int64(VideoFrameTick)
		ts := uint32(abs) // wraps modulo 2^32 by construction
		emit(PTVideoHigh, SSRCHigh, ts, []byte{0x10, byte(seq >> 8), byte(seq)})
		emit(PTVideoLow, SSRCLow, ts, []byte{0x20, byte(seq >> 8), byte(seq)})
	}
	// Audio: 48 kHz clock, one packet per video frame interval.
	for seq := uint32(0); seq < uint32(s.VideoFrames); seq++ {
		abs90 := int64(base) + int64(seq)*int64(VideoFrameTick)
		ts48 := uint32(clock.Tick(abs90).Scale(AudioHz))
		emit(PTAudio, SSRCAud, ts48, []byte{0x30, byte(seq)})
	}
	// Timed metadata events in plan order, with planned delays applied and
	// drops removed and duplicates repeated.
	for _, it := range plan {
		if it.Drop {
			continue
		}
		tag, ok := metaTag[it.Track]
		if !ok {
			return nil, fmt.Errorf("fixture: no RTP tag for track %q", it.Track)
		}
		payload := encodeMeta(tag, it)
		observed := int64(it.ExpectedTick) + int64(it.DelayTicks)
		ts := uint32(int64(base) + observed)
		emit(PTMeta, SSRCMet, ts, payload)
		if it.Duplicate {
			emit(PTMeta, SSRCMet, ts, payload)
		}
	}

	if s.Fault == FaultTruncate {
		// Cut halfway through the packet records; compute the on-disk offset.
		cap.TruncatedByte = cap.bytesUntil(len(cap.Packets) / 2)
	}
	return cap, nil
}

func encodeMeta(tag byte, it model.PlanItem) []byte {
	var b bytes.Buffer
	b.WriteByte(tag)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], it.Seq)
	b.Write(tmp[:])
	keyb := []byte(it.Key)
	binary.BigEndian.PutUint32(tmp[:], uint32(len(keyb)))
	b.Write(tmp[:])
	b.Write(keyb)
	return b.Bytes()
}

// Marshal serializes the capture with a magic header and u16 length prefixes.
func (c *Capture) Marshal() ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("RGRT")
	var nbuf [4]byte
	binary.BigEndian.PutUint32(nbuf[:], uint32(len(c.Packets)))
	out.Write(nbuf[:])
	for _, pkt := range c.Packets {
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint32(nbuf[:], uint32(len(raw)))
		out.Write(nbuf[:])
		out.Write(raw)
	}
	return out.Bytes(), nil
}

// bytesUntil returns the serialized byte count up to (not including) the
// packet record at index idx, including the 8-byte file header.
func (c *Capture) bytesUntil(idx int) int64 {
	total := int64(8)
	for i := 0; i < idx && i < len(c.Packets); i++ {
		raw, err := c.Packets[i].Marshal()
		if err != nil {
			panic(err) // generated packets always marshal
		}
		total += int64(4 + len(raw))
	}
	return total
}
