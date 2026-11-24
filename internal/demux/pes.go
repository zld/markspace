package demux

import (
	"encoding/binary"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

// pesBuffer accumulates the transport payload of one PID between
// payload_unit_start_indicator flags.
type pesBuffer struct {
	pid    uint16
	orig   int64
	cursor int64
	first  int64
	data   []byte
	active bool
}

type tsPES struct {
	pid     uint16
	offset  int64
	payload []byte
	pts     uint64
	dts     uint64
	hasPTS  bool
	hasDTS  bool
}

// assemblePES walks the packets again, rebuilding each PES packet.  The buffer
// is flushed on every payload_unit_start_indicator and at end of input, so no
// packet payload is silently discarded.
func assemblePES(data []byte, psi *tsPSI, media *Media) ([]Track, []metadata.Event, error) {
	buffers := make(map[uint16]*pesBuffer)
	var order []uint16
	var pids []uint16
	for pid := range psi.streams {
		pids = append(pids, pid)
	}
	sortUint16(pids)
	var finished []tsPES
	flush := func(pid uint16) {
		buf, ok := buffers[pid]
		if !ok || len(buf.data) == 0 {
			return
		}
		if pes, ok := parsePES(buf); ok {
			finished = append(finished, pes)
		}
		buf.data = buf.data[:0]
		buf.active = false
	}
	pos := 0
	for pos < len(data) {
		if pos+tsPacketSize > len(data) {
			break
		}
		if data[pos] != tsSyncByte {
			break
		}
		pkt := data[pos : pos+tsPacketSize]
		pid := binary.BigEndian.Uint16(pkt[1:]) & 0x1fff
		afc := (pkt[3] >> 4) & 3
		if afc&1 == 0 {
			pos += tsPacketSize
			continue
		}
		payloadStart := 4
		if afc&2 != 0 {
			payloadStart = 5 + int(pkt[4])
		}
		if payloadStart >= tsPacketSize {
			pos += tsPacketSize
			continue
		}
		payload := pkt[payloadStart:]
		if _, ok := psi.streams[pid]; !ok || len(payload) == 0 {
			pos += tsPacketSize
			continue
		}
		pusi := pkt[1]&0x40 != 0
		buf, exists := buffers[pid]
		if !exists {
			buf = &pesBuffer{pid: pid}
			buffers[pid] = buf
			order = append(order, pid)
		}
		if pusi {
			flush(pid)
			buf.active = true
			buf.first = int64(pos + payloadStart)
		}
		if !buf.active {
			pos += tsPacketSize
			continue
		}
		buf.data = append(buf.data, payload...)
		pos += tsPacketSize
	}
	for _, pid := range order {
		flush(pid)
	}

	byID := make(map[uint16]*Track)
	var tracks []Track
	for _, pid := range pids {
		st := psi.streams[pid]
		tr := Track{
			ID: uint32(pid), Kind: st.kind, Timescale: tsTimescale,
			Codec: st.codec, Handler: tsHandler(st.kind),
		}
		tracks = append(tracks, tr)
	}
	for i := range tracks {
		byID[uint16(tracks[i].ID)] = &tracks[i]
	}
	lastDTS := make(map[uint16]uint64)
	for _, pes := range finished {
		tr := byID[pes.pid]
		if tr == nil || !pes.hasPTS {
			continue
		}
		dts := pes.pts
		if pes.hasDTS {
			dts = pes.dts
		}
		var delta uint32
		if prev, ok := lastDTS[pes.pid]; ok && dts >= prev {
			delta = uint32(dts - prev)
		}
		lastDTS[pes.pid] = dts
		tr.Samples = append(tr.Samples, Sample{
			Offset: pes.offset,
			Size:   uint32(len(pes.payload)),
			Delta:  delta,
			DTS:    clock.New(int64(dts), tsTimescale),
			CTS:    clock.New(int64(pes.pts), tsTimescale),
			Sync:   true,
			Data:   pes.payload,
		})
	}
	events := make([]metadata.Event, 0, len(finished))
	for ti := range tracks {
		tr := &tracks[ti]
		switch {
		case tr.Kind == track.KindMetadata && tr.Codec == "id3":
			for si := range tr.Samples {
				s := &tr.Samples[si]
				if len(s.Data) < 10 || string(s.Data[:3]) != "ID3" {
					continue
				}
				events = append(events, metadata.Event{
					Kind: metadata.KindID3, TrackID: tr.ID, Time: s.CTS,
					Data: s.Data[:id3Size(s.Data)], SourceOffset: s.Offset,
				})
			}
		case tr.Kind == track.KindSubtitle:
			for si := range tr.Samples {
				s := &tr.Samples[si]
				if len(s.Data) == 0 {
					continue
				}
				events = append(events, metadata.Event{
					Kind: metadata.KindSubtitle, TrackID: tr.ID, Time: s.CTS,
					Data: s.Data, SourceOffset: s.Offset,
				})
			}
		case tr.Kind == track.KindVideo && tr.Codec == "avc":
			events = append(events, annexBSEIEvents(tr)...)
		}
	}
	if len(media.Cut) > 0 {
		kept := events[:0]
		for _, e := range events {
			if e.SourceOffset < media.Cut[0].Start {
				kept = append(kept, e)
			}
		}
		events = kept
	}
	// Drop tracks that produced no complete PES: an empty mapped track would
	// fail validation later with a less specific error.
	keptTracks := tracks[:0]
	for _, tr := range tracks {
		if len(tr.Samples) > 0 {
			keptTracks = append(keptTracks, tr)
		}
	}
	return keptTracks, events, nil
}
