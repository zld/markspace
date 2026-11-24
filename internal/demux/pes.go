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

func parsePES(buf *pesBuffer) (tsPES, bool) {
	d := buf.data
	if len(d) < 9 || d[0] != 0 || d[1] != 0 || d[2] != 1 {
		return tsPES{}, false
	}
	headerLen := int(d[8])
	if 9+headerLen > len(d) {
		return tsPES{}, false
	}
	flags := d[7]
	payload := d[9+headerLen:]
	if declared := int(binary.BigEndian.Uint16(d[4:6])); declared != 0 {
		// PES_packet_length counts the optional header plus payload.  Trim any
		// transport stuffing that followed the declared packet.
		want := declared - 3 - headerLen
		if want < 0 {
			return tsPES{}, false
		}
		if want < len(payload) {
			payload = payload[:want]
		}
	}
	pes := tsPES{pid: buf.pid, offset: buf.first, payload: payload}
	if flags&0x80 != 0 && headerLen >= 5 {
		pes.pts = readPTS(d[9:14])
		pes.hasPTS = true
	}
	if flags&0x40 != 0 && headerLen >= 10 {
		pes.dts = readPTS(d[14:19])
		pes.hasDTS = true
	}
	return pes, true
}

func readPTS(b []byte) uint64 {
	v := uint64((b[0]>>1)&0x07) << 30
	v |= uint64(b[1]) << 22
	v |= uint64(b[2]>>1) << 15
	v |= uint64(b[3]) << 7
	v |= uint64(b[4] >> 1)
	return v
}

func tsHandler(k track.Kind) string {
	switch k {
	case track.KindVideo:
		return "vide"
	case track.KindAudio:
		return "soun"
	case track.KindSubtitle:
		return "subt"
	case track.KindChapter:
		return "text"
	case track.KindMetadata:
		return "meta"
	}
	return ""
}

func sortUint16(v []uint16) {
	for i := 0; i < len(v); i++ {
		for j := i + 1; j < len(v); j++ {
			if v[j] < v[i] {
				v[i], v[j] = v[j], v[i]
			}
		}
	}
}

type annexBNAL struct {
	off  int
	data []byte
}

// annexBNALUs splits an Annex B byte stream into NAL units.
func annexBNALUs(b []byte) []annexBNAL {
	var out []annexBNAL
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				out = append(out, annexBNAL{off: start, data: trimZeros(b[start:i])})
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, annexBNAL{off: start, data: trimZeros(b[start:])})
	}
	return out
}

func trimZeros(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}
