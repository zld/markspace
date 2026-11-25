package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"archive.example/mediakernel/internal/metadata"
)

// Archive is one generated input set: an MP4 metadata document plus an RTP
// packet stream. Bytes are produced deterministically from Seed and Kind.
type Archive struct {
	Kind      Kind
	Seed      int64
	MP4       []byte
	Truncated bool
	// TruncatedAt is the byte offset at which the MP4 document was cut, when
	// Truncated is true.
	TruncatedAt int64
	RTP         []RTPPacket
	// Notes explains what was deliberately damaged, for the terminal summary.
	Notes []string
}

// RTPPacket is a wire-framed RTP packet with its byte offset in the stream.
type RTPPacket struct {
	Bytes  []byte
	Offset int64
}

// Build produces the archive for a kind and seed.
func Build(kind Kind, seed int64) (Archive, error) {
	rng := rand.New(rand.NewSource(seed))
	items := itemsFor(kind, rng)
	trafs := groupFragments(items)
	// Each emsg gets its own moof, placed between the media fragments so it
	// remains independent of any track's timescale and ordering.
	for _, es := range buildEmsgSpecs() {
		trafs = append(trafs, traf{emsgBox: EmsgBox(es.Scheme, es.Value, EmsgTimescale,
			es.Presentation, es.Duration, uint32(es.Sequence), es.Payload)})
	}
	mp4 := buildMP4(DefaultTracks(), trafs)

	arc := Archive{Kind: kind, Seed: seed, MP4: mp4}
	switch kind {
	case KindTruncated:
		cut := int64(len(mp4)) - 37 // cut inside the final fragment
		if cut < 8 {
			cut = int64(len(mp4)) / 2
		}
		arc.MP4 = mp4[:cut]
		arc.Truncated = true
		arc.TruncatedAt = cut
		arc.Notes = append(arc.Notes, fmt.Sprintf("mp4 truncated at byte %d of %d", cut, len(mp4)))
	}

	rtpPackets, notes, err := buildRTP(kind, seed, items)
	if err != nil {
		return Archive{}, err
	}
	arc.RTP = rtpPackets
	arc.Notes = append(arc.Notes, notes...)
	return arc, nil
}

// buildRTP turns the metadata item schedule into a deterministic RTP packet
// stream. Each packet's payload is a compact self-describing record so the
// parser can recover both identity and time without external state.
func buildRTP(kind Kind, seed int64, items []TimedItem) ([]RTPPacket, []string, error) {
	rng := rand.New(rand.NewSource(seed ^ 0x5EED))

	const (
		baseSeq  = uint32(1000)
		ssrc     = uint32(0x16020001)
		pt       = uint8(96)
		tickRate = int64(90000)
	)
	var logical []RTPPacket
	var seq uint32
	var notes []string
	// Choose a base timestamp near the 32-bit wrap to exercise unwrapping.
	baseTS := uint32(0xFFFF_F000)

	appendPacket := func(ts uint32, payload []byte) {
		pkt := buildPacket(baseSeq+seq, ts, ssrc, pt, payload)
		logical = append(logical, RTPPacket{Bytes: pkt})
		seq++
	}

	for _, it := range items {
		// The item's nanosecond instant is derived from its own track scale.
		ns := nsFromTicks(it.Presentation, timescaleFor(it.TrackID))
		ts := baseTS + uint32(ns*tickRate/1_000_000_000)
		payload := rtpPayload(it)
		appendPacket(ts, payload)
	}

	switch kind {
	case KindDamaged:
		var damaged []RTPPacket
		i := 0
		for i < len(logical) {
			switch rng.Intn(10) {
			case 0, 1: // duplicate the current packet
				damaged = append(damaged, logical[i], logical[i])
				i++
			case 2: // reorder with the next packet
				if i+1 < len(logical) {
					damaged = append(damaged, logical[i+1], logical[i])
					i += 2
				} else {
					damaged = append(damaged, logical[i])
					i++
				}
			case 3: // burst loss: drop the next three packets
				i += 3
				notes = append(notes, fmt.Sprintf("burst loss dropped %d packets at index %d", minInt(3, len(logical)-i+3), i))
			default:
				damaged = append(damaged, logical[i])
				i++
			}
		}
		logical = damaged
		notes = append(notes, "rtp duplicates, reordering and burst loss injected")
	case KindLate:
		// Deliver every fifth packet at the end of the stream, after their
		// holes have been declared lost. The reorderer will queue them as late
		// and the timeline will replay them.
		var early, late []RTPPacket
		for i, p := range logical {
			if i%5 == 3 {
				late = append(late, p)
				continue
			}
			early = append(early, p)
		}
		logical = append(early, late...)
		notes = append(notes, fmt.Sprintf("rtp %d packets delayed to end of stream", len(late)))
	}

	// Assign byte offsets as if the packets were concatenated on the wire.
	var off int64
	for i := range logical {
		logical[i].Offset = off
		off += int64(len(logical[i].Bytes))
	}
	return logical, notes, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func timescaleFor(trackID uint32) uint32 {
	for _, t := range DefaultTracks() {
		if t.TrackID == trackID {
			return t.Timescale
		}
	}
	return 1000
}

// nsFromTicks is the fixture-side inverse of the clock mapping, used only to
// schedule RTP timestamps at the same media instants.
func nsFromTicks(t uint64, timescale uint32) int64 {
	if timescale == 0 {
		return 0
	}
	return int64(t) * 1_000_000_000 / int64(timescale)
}

// buildPacket marshals a minimal RTP packet with pion-compatible framing.
func buildPacket(seq, ts, ssrc uint32, pt uint8, payload []byte) []byte {
	out := make([]byte, 12+len(payload))
	out[0] = 0x80 // version 2, no padding/extension/CSRC
	out[1] = pt   // marker 0, payload type
	binary.BigEndian.PutUint16(out[2:4], uint16(seq))
	binary.BigEndian.PutUint32(out[4:8], ts)
	binary.BigEndian.PutUint32(out[8:12], ssrc)
	copy(out[12:], payload)
	return out
}

// rtpPayload encodes the item as a compact record: version, track id, kind byte,
// presentation ticks (per the item's track scale) and text.
func rtpPayload(it TimedItem) []byte {
	p := make([]byte, 16, 16+len(it.Text)+1)
	p[0] = 1
	p[1] = byte(it.TrackID)
	p[2] = kindByte(it.BoxType)
	p[3] = 0 // reserved
	binary.BigEndian.PutUint64(p[4:12], it.Presentation)
	binary.BigEndian.PutUint32(p[12:16], it.Duration)
	p = append(p, []byte(it.Text)...)
	p = append(p, 0)
	return p
}

func kindByte(boxType string) byte {
	switch boxType {
	case metadata.BoxSubtitle:
		return 1
	case metadata.BoxChapter:
		return 2
	case metadata.BoxID3:
		return 3
	case metadata.BoxSEI:
		return 4
	default:
		return 0
	}
}
