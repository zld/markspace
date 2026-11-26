package ingest

import (
	"audionorm/internal/media"
	"errors"

	"github.com/pion/rtp"
)

// RTPIngester consumes raw RTP packets of one codec and rebuilds the
// unified 48 kHz timeline. It is single-goroutine and stateful: feed
// packets in arrival order via Push, then call Close to finalize.
type RTPIngester struct {
	codec      Codec
	clockRate  uint64
	channels   uint16
	conv       *media.RateConverter
	res        *Result

	init       bool
	maxSeq16   uint16
	cycles     uint64
	maxExt     uint64
	seen       map[uint64]struct{}
	lastRTP    uint32
	pending    *Packet
	pendingSeq uint64
}

// NewRTPIngester builds an ingester. Opus uses a 48 kHz RTP clock;
// AAC-LC uses its native sample-rate clock (e.g. 44100/48000).
func NewRTPIngester(codec Codec, clockRate uint32, channels uint16) *RTPIngester {
	if channels != 1 && channels != 2 {
		// Layout rejection is surfaced through the error log on first use.
		channels = 0
	}
	return &RTPIngester{
		codec:     codec,
		clockRate: uint64(clockRate),
		channels:  channels,
		conv:      media.NewRateConverter(uint64(clockRate)),
		res:       NewResult(),
		seen:      map[uint64]struct{}{},
	}
}

// updateSeq implements the RFC 3550 Appendix A.1 extended-sequence logic
// and returns the extended seq plus how the packet must be classified.
type seqClass int

const (
	seqFirst seqClass = iota
	seqForward
	seqDuplicate
	seqLate
)

func (in *RTPIngester) updateSeq(seq uint16) (uint64, seqClass) {
	if !in.init {
		in.init = true
		in.maxSeq16 = seq
		in.maxExt = uint64(seq)
		in.seen[uint64(seq)] = struct{}{}
		return in.maxExt, seqFirst
	}
	udelta := seq - in.maxSeq16
	switch {
	case udelta == 0:
		return in.maxExt, seqDuplicate
	case udelta < 1<<15: // forward, possibly wrapping
		if seq < in.maxSeq16 {
			in.cycles += 1 << 16
		}
		in.maxSeq16 = seq
		in.maxExt = in.cycles + uint64(seq)
		if _, ok := in.seen[in.maxExt]; ok {
			return in.maxExt, seqDuplicate
		}
		in.seen[in.maxExt] = struct{}{}
		return in.maxExt, seqForward
	default: // very late packet from an older cycle
		ext := in.cycles + uint64(seq)
		if _, ok := in.seen[ext]; ok {
			return ext, seqDuplicate
		}
		return ext, seqLate
	}
}

// Push processes one raw RTP packet located at byteOffset in the stream.
func (in *RTPIngester) Push(raw []byte, byteOffset int64) error {
	if len(raw) > media.MaxPacketBytes {
		in.res.Errors.Add(media.NewPendingError(media.ErrPacketTooLarge,
			"rtp packet exceeds 1 MiB", 0, byteOffset))
		return nil
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		in.res.Errors.Add(media.NewPendingError(media.ErrRTPParse,
			err.Error(), 0, byteOffset))
		return nil
	}
	ext, class := in.updateSeq(pkt.SequenceNumber)
	switch class {
	case seqDuplicate:
		// Duplicates carry no time and never advance the timeline.
		in.res.Duplicates++
		return nil
	case seqLate:
		in.res.Errors.Add(media.NewPendingError(media.ErrExtendedSeq,
			"late packet from an older sequence cycle", ext, byteOffset))
		return nil
	}

	if in.channels == 0 {
		in.res.Errors.Add(media.NewPendingError(media.ErrUnsupportedLayout,
			"only mono or stereo AAC-LC/Opus is accepted", ext, byteOffset))
		return nil
	}
	if err := in.validatePayload(ext, byteOffset, pkt.Payload); err != nil {
		// Corrupt payload: duration unknown, interval stays HOLD.
		return nil
	}

	if !in.placeForward(ext, byteOffset, &pkt) {
		return nil
	}
	return nil
}

// placeForward maps an accepted packet onto the timeline from RTP
// timestamp deltas (which already account for lost packets); the
// RateConverter retains the sub-sample remainder across packets.
func (in *RTPIngester) placeForward(ext uint64, off int64, pkt *rtp.Packet) bool {
	var start uint64
	if in.pending == nil {
		start = 0
	} else {
		delta := pkt.Timestamp - in.lastRTP // uint32 wrap subtraction
		in.conv.Convert(uint64(delta))
		start = in.conv.OutputOffset()
		missing := ext - in.pendingSeq - 1
		if missing > 0 {
			in.res.Gaps = append(in.res.Gaps, Gap{
				ExtSeq: in.pendingSeq + 1,
				When:   media.Range{Start: in.pending.When.Start + in.pendingDuration(), End: start},
			})
		}
		in.pending.When.End = start
		in.res.Packets[len(in.res.Packets)-1].When = in.pending.When
	}
	in.lastRTP = pkt.Timestamp
	in.pending = &Packet{
		Codec: in.codec, ExtSeq: ext, Payload: append([]byte(nil), pkt.Payload...),
		When: media.Range{Start: start}, ByteOffset: off,
		SampleRate: uint32(in.clockRate), Channels: in.channels,
	}
	in.pendingSeq = ext
	in.res.Packets = append(in.res.Packets, *in.pending)
	return true
}

// pendingDuration returns the coded duration of the currently open
// packet in timeline samples, without mutating the converter state.
func (in *RTPIngester) pendingDuration() uint64 {
	if in.pending == nil {
		return 0
	}
	var seconds float64
	switch in.codec {
	case CodecOpus:
		s, err := OpusDuration(in.pending.Payload)
		if err != nil {
			return 0
		}
		seconds = s
	case CodecAAC:
		seconds = 1024.0 / float64(in.clockRate)
	}
	return uint64(seconds*media.TimelineRate + 0.5)
}

func (in *RTPIngester) validatePayload(ext uint64, off int64, payload []byte) error {
	switch in.codec {
	case CodecOpus:
		if _, err := OpusDuration(payload); err != nil {
			in.res.Errors.Add(media.NewPendingError(media.ErrCorruptPacket,
				err.Error(), ext, off))
			return err
		}
	case CodecAAC:
		if err := validateAACRTP(payload); err != nil {
			in.res.Errors.Add(media.NewPendingError(media.ErrCorruptPacket,
				err.Error(), ext, off))
			return err
		}
	default:
		in.res.Errors.Add(media.NewPendingError(media.ErrUnsupportedCodec,
			string(in.codec), ext, off))
		return errors.New("unsupported codec")
	}
	return nil
}

// Close finalizes the last packet's extent from its coded duration and
// returns the complete ingest result.
func (in *RTPIngester) Close() (*Result, error) {
	if in.pending != nil {
		var seconds float64
		var err error
		switch in.codec {
		case CodecOpus:
			seconds, err = OpusDuration(in.pending.Payload)
		case CodecAAC:
			seconds = 1024.0 / float64(in.clockRate)
		}
		if err == nil {
			n := uint64(seconds*media.TimelineRate + 0.5)
			in.pending.When.End = in.pending.When.Start + n
			in.res.Packets[len(in.res.Packets)-1].When = in.pending.When
		}
	}
	return in.res, nil
}
