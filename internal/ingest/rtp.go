package ingest

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pion/rtp"

	"replaycheck/internal/model"
)

// RTPEvent is one decoded replay event derived from an RTP packet.
type RTPEvent struct {
	Index     int    `json:"index"`
	SSRC      uint32 `json:"ssrc"`
	Sequence  uint16 `json:"sequence"`
	Timestamp uint32 `json:"timestamp"`
	Unwrapped uint64 `json:"unwrapped"`
	Wraps     uint64 `json:"wraps"`
	Offset    int64  `json:"offset"` // byte offset of the record in the events file
	Payload   []byte `json:"-"`
	Marker    bool   `json:"marker"`
}

// RTPStream is a set of events for one SSRC with a single unwrap lineage.
type RTPStream struct {
	SSRC         uint32     `json:"ssrc"`
	ClockRate    uint32     `json:"clockRate"`
	Events       []RTPEvent `json:"events"`
	Wraps        uint64     `json:"wraps"`
	Backwards    int        `json:"backwards"`
	First        uint64     `json:"first"`
	Last         uint64     `json:"last"`
	SourceDigest string     `json:"sourceDigest"`
}

// EventFile is the parsed sidecar of RTP events.
type EventFile struct {
	Path    string       `json:"path"`
	Digest  string       `json:"digest"`
	Streams []*RTPStream `json:"streams"`
}

// ParseEvents reads a line-delimited hex RTP capture. Comments (#) and blank
// lines are ignored. Each record is decoded with pion/rtp; a decode failure
// reports the byte offset of that record.
func ParseEvents(path string, clockRate uint32) (*EventFile, error) {
	ref := model.TrackRef{Kind: "rtp", Name: filepath.Base(path)}
	st, err := os.Stat(path)
	if err != nil {
		return nil, model.WrapError(model.StageIngest, "events", ref, 0, "cannot stat events", err)
	}
	if !st.Mode().IsRegular() {
		return nil, model.NewError(model.StageIngest, "events", ref, 0, "events input is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.WrapError(model.StageIngest, "events", ref, 0, "cannot read events", err)
	}
	ef := &EventFile{Path: path, Digest: model.SHA256Hex(raw)}
	if clockRate == 0 {
		return nil, model.NewError(model.StageIngest, "events", ref, 0, "clock rate must be positive")
	}

	bySSRC := map[uint32]*RTPStream{}
	unwrap := map[uint32]*model.UnwrapState{}
	var offset int64
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	idx := 0
	for sc.Scan() {
		lineOff := offset
		line := sc.Text()
		offset += int64(len(sc.Bytes())) + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		wire, err := hex.DecodeString(trimmed)
		if err != nil {
			return nil, model.WrapError(model.StageIngest, "events", ref, uint64(lineOff), fmt.Sprintf("record %d is not hex", idx), err)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(wire); err != nil {
			return nil, model.WrapError(model.StageIngest, "events", ref, uint64(lineOff), fmt.Sprintf("record %d is not RTP", idx), err)
		}
		stream := bySSRC[pkt.SSRC]
		if stream == nil {
			stream = &RTPStream{SSRC: pkt.SSRC, ClockRate: clockRate, SourceDigest: ef.Digest}
			bySSRC[pkt.SSRC] = stream
			u := &model.UnwrapState{}
			u.Init(pkt.Timestamp)
			unwrap[pkt.SSRC] = u
			stream.First = uint64(pkt.Timestamp)
		}
		u := unwrap[pkt.SSRC]
		res, err := u.Push(pkt.Timestamp)
		if err != nil {
			stream.Backwards++
			return nil, model.WrapError(model.StageIngest, "rtp", model.TrackRef{ID: pkt.SSRC, Kind: "ssrc"}, uint64(lineOff),
				fmt.Sprintf("sequence %d timestamp %d went backwards (delta %d)", pkt.SequenceNumber, pkt.Timestamp, res.Delta), err)
		}
		ev := RTPEvent{
			Index:     len(stream.Events),
			SSRC:      pkt.SSRC,
			Sequence:  pkt.SequenceNumber,
			Timestamp: pkt.Timestamp,
			Unwrapped: res.Value,
			Wraps:     res.Wraps,
			Offset:    lineOff,
			Payload:   append([]byte(nil), pkt.Payload...),
			Marker:    pkt.Marker,
		}
		stream.Events = append(stream.Events, ev)
		stream.Wraps = res.Wraps
		stream.Last = res.Value
		idx++
	}
	if err := sc.Err(); err != nil {
		return nil, model.WrapError(model.StageIngest, "events", ref, 0, "cannot scan events", err)
	}
	if len(bySSRC) == 0 {
		return nil, model.NewError(model.StageIngest, "events", ref, 0, "no RTP events")
	}
	for _, s := range bySSRC {
		ef.Streams = append(ef.Streams, s)
	}
	sortStreams(ef.Streams)
	return ef, nil
}

func sortStreams(s []*RTPStream) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].SSRC < s[j-1].SSRC; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
