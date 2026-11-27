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
	haveFirst  bool
	nextSample uint32
}

// ReadRTP streams the length-prefixed RTP capture at path one packet at a
// time, expanding 32-bit timestamp wraparound into absolute master ticks on a
// single capture-wide time line. The first packet seen defines the global
// origin; every other SSRC is anchored to it through its own clock rate, so a
// 48 kHz audio track and a 90 kHz video/metadata track keep their true
// presentation offset. No packet is buffered, so memory stays bounded by one
// packet and one growing sample/event list per stream.
func ReadRTP(path string) (*RTPResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.StageReceive, "open", path, "", 0, err)
	}
	defer f.Close()

	res := &RTPResult{}
	states := map[uint32]*streamState{}
	table := ssrcTable()
	metaTable := metaTagTrack()

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
	var anchor clock.Tick
	anchorSet := false
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
		recOff := pos
		pos += int64(n)

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, model.NewError(model.StageParse, "rtp-unmarshal", path,
				"", recOff, err)
		}
		meta, ok := table[pkt.SSRC]
		if !ok {
			return nil, model.NewError(model.StageParse, "ssrc", path, "",
				recOff, fmt.Errorf("unknown SSRC %#x", pkt.SSRC))
		}
		if !anchorSet {
			// The very first packet is time zero for the whole capture.
			master, err := clock.Convert(int64(pkt.Timestamp), meta.hz)
			if err != nil {
				return nil, model.NewError(model.StageParse, "anchor", path,
					meta.trackID, recOff, err)
			}
			anchor, anchorSet = master, true
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
		if _, err := st.consume(pkt, anchor, metaTable); err != nil {
			return nil, model.NewError(model.StageParse, "unwrap", path,
				meta.trackID, recOff, err)
		}
	}
	if !anchorSet {
		return nil, model.NewError(model.StageParse, "no-streams", path, "", 0,
			fmt.Errorf("%w: capture holds no packets", model.ErrTruncated))
	}
	return res, nil
}

func truncErr(path, track string, off int64, cause error) error {
	return model.NewError(model.StageParse, "truncated-capture", path, track, off,
		fmt.Errorf("%w: %v", model.ErrTruncated, cause))
}

// consume expands one packet on the capture-wide time line. anchor is the
// global origin in master ticks; this SSRC's origin is that same instant
// expressed in the SSRC's own clock, so the raw delta divides cleanly.
func (s *streamState) consume(pkt *rtp.Packet, anchor clock.Tick,
	metaTable map[byte]struct {
		id   string
		kind model.TrackKind
	}) (clock.Tick, error) {
	origin := anchor.Scale(s.hz)
	var abs int64
	if !s.haveFirst {
		abs = int64(pkt.Timestamp)
		s.haveFirst = true
	} else {
		abs = clock.Unwrap(s.prevAbs, uint64(pkt.Timestamp),
			clock.RTPTimestampMod)
	}
	s.prevAbs = abs

	tick, err := clock.Convert(abs-origin, s.hz)
	if err != nil {
		return 0, err
	}

	if s.out.Kind == model.KindID3 && pkt.PayloadType == fixture.PTMeta {
		return tick, s.consumeMeta(pkt, tick, metaTable)
	}
	seq := s.nextSample
	s.nextSample++
	s.out.Samples = append(s.out.Samples, model.Sample{Seq: seq, Tick: tick})
	return tick, nil
}

func (s *streamState) consumeMeta(pkt *rtp.Packet, tick clock.Tick,
	tagTable map[byte]struct {
		id   string
		kind model.TrackKind
	}) error {
	if len(pkt.Payload) < 9 {
		return fmt.Errorf("%w: meta payload %d bytes", model.ErrTruncated, len(pkt.Payload))
	}
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
		ObservedTick: tick, PayloadHash: sum,
	})
	return nil
}
