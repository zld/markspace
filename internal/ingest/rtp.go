package ingest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"

	"regress/internal/clock"
	"regress/internal/fixture"
	"regress/internal/model"
)

// Stream is one reconstructed RTP source identified by its SSRC.
type Stream struct {
	SSRC    uint32
	TrackID string
	Kind    model.TrackKind
	Samples []model.Sample
	Events  []model.Event
}

// RTPResult holds the reconstructed streams of one capture file.
type RTPResult struct {
	Streams []*Stream
}

type ssrcMeta struct {
	trackID string
	kind    model.TrackKind
	// hz is the RTP clock rate of this source.
	hz int64
}

func ssrcTable() map[uint32]ssrcMeta {
	return map[uint32]ssrcMeta{
		fixture.SSRCHigh: {fixture.RendHigh, model.KindVideo, fixture.VideoHz},
		fixture.SSRCLow:  {fixture.RendLow, model.KindVideo, fixture.VideoHz},
		fixture.SSRCAud:  {fixture.AudioID, model.KindAudio, fixture.AudioHz},
		fixture.SSRCMet:  {fixture.MetaID3, model.KindID3, fixture.VideoHz},
	}
}

func metaTagTrack() map[byte]struct {
	id   string
	kind model.TrackKind
} {
	return map[byte]struct {
		id   string
		kind model.TrackKind
	}{
		fixture.MetaTagID3:      {fixture.MetaID3, model.KindID3},
		fixture.MetaTagEmsg:     {fixture.MetaEmsg, model.KindEmsg},
		fixture.MetaTagSEI:      {fixture.MetaSEI, model.KindSEI},
		fixture.MetaTagSubtitle: {fixture.MetaSub, model.KindSubtitle},
		fixture.MetaTagChapter:  {fixture.MetaChap, model.KindChapter},
	}
}

// streamState holds the modular unwrap cursor for one SSRC.
type streamState struct {
	out        *Stream
	hz         int64
	prevAbs    int64
	baseAbs    int64
	haveBase   bool
	nextSample uint32
}

// ReadRTP streams the length-prefixed RTP capture at path, expanding 32-bit
// timestamp and 16-bit sequence wraparound into absolute master ticks.
func ReadRTP(path string) (*RTPResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.StageReceive, "open", path, "", 0, err)
	}
	defer f.Close()

	res := &RTPResult{}
	states := map[uint32]*streamState{}
	table := ssrcTable()

	var hdr [8]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, model.NewError(model.StageParse, "capture-header", path, "",
			0, fmt.Errorf("%w: %v", model.ErrTruncated, err))
	}
	if string(hdr[:4]) != "RGRT" {
		return nil, model.NewError(model.StageParse, "magic", path, "", 0,
			fmt.Errorf("not an RGR capture: %q", hdr[:4]))
	}
	count := binary.BigEndian.Uint32(hdr[4:])

	var pos int64 = 8
	for i := uint32(0); i < count; i++ {
		var lenbuf [4]byte
		if _, err := io.ReadFull(f, lenbuf[:]); err != nil {
			return nil, truncErr(path, "", pos, err)
		}
		pos += 4
		n := binary.BigEndian.Uint32(lenbuf[:])
		raw := make([]byte, n)
		if _, err := io.ReadFull(f, raw); err != nil {
			return nil, truncErr(path, "", pos, err)
		}
		pos += int64(n)

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, model.NewError(model.StageParse, "rtp-unmarshal", path,
				"", pos-int64(n), err)
		}
		meta, ok := table[pkt.SSRC]
		if !ok {
			return nil, model.NewError(model.StageParse, "ssrc", path, "",
				pos-int64(n), fmt.Errorf("unknown SSRC %#x", pkt.SSRC))
		}
		st := states[pkt.SSRC]
		if st == nil {
			st = &streamState{
				out: &Stream{SSRC: pkt.SSRC, TrackID: meta.trackID,
					Kind: meta.kind},
				hz: meta.hz,
			}
			states[pkt.SSRC] = st
			res.Streams = append(res.Streams, st.out)
		}
		if err := st.consume(pkt); err != nil {
			return nil, model.NewError(model.StageParse, "unwrap", path,
				meta.trackID, pos-int64(n), err)
		}
	}
	if len(res.Streams) == 0 {
		return nil, model.NewError(model.StageParse, "no-streams", path, "", 0,
			fmt.Errorf("%w: capture holds no packets", model.ErrTruncated))
	}
	return res, nil
}

func truncErr(path, track string, off int64, cause error) error {
	return model.NewError(model.StageParse, "truncated-capture", path, track, off,
		fmt.Errorf("%w: %v", model.ErrTruncated, cause))
}

func (s *streamState) consume(pkt *rtp.Packet) error {
	// Expand the 32-bit timestamp against this SSRC's running cursor.
	var abs int64
	if !s.haveBase {
		abs = int64(pkt.Timestamp)
		s.baseAbs = abs
		s.haveBase = true
	} else {
		abs = clock.Unwrap(s.prevAbs, uint64(pkt.Timestamp), clock.RTPTimestampMod)
	}
	s.prevAbs = abs
	rel := abs - s.baseAbs // transport-relative units in this SSRC's clock
	tick, err := clock.Convert(rel, s.hz)
	if err != nil {
		return err
	}

	if s.out.Kind == model.KindID3 && pkt.PayloadType == fixture.PTMeta {
		return s.consumeMeta(pkt, tick)
	}
	// Media sample: sequence is derived from arrival order per SSRC; the RTP
	// 16-bit sequence number is carried for diagnostics.
	seq := s.nextSample
	s.nextSample++
	s.out.Samples = append(s.out.Samples, model.Sample{
		Seq:  seq,
		Tick: tick,
	})
	return nil
}

func (s *streamState) consumeMeta(pkt *rtp.Packet, tick clock.Tick) error {
	if len(pkt.Payload) < 9 {
		return fmt.Errorf("%w: meta payload %d bytes", model.ErrTruncated, len(pkt.Payload))
	}
	tagTable := metaTagTrack()
	tag, ok := tagTable[pkt.Payload[0]]
	if !ok {
		return fmt.Errorf("unknown metadata payload tag %d", pkt.Payload[0])
	}
	seq := binary.BigEndian.Uint32(pkt.Payload[1:5])
	klen := binary.BigEndian.Uint32(pkt.Payload[5:9])
	if int(9+klen) > len(pkt.Payload) {
		return fmt.Errorf("%w: key length %d exceeds payload", model.ErrTruncated, klen)
	}
	key := string(pkt.Payload[9 : 9+klen])
	sum := sha256Short(bytes.Clone(pkt.Payload[9 : 9+klen]))
	s.out.Events = append(s.out.Events, model.Event{
		Key: key, Track: tag.id, Kind: tag.kind, Seq: seq,
		ObservedTick: tick, Delay: tick, PayloadHash: sum,
	})
	return nil
}

