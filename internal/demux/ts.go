package demux

import (
	"encoding/binary"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsTimescale  = 90000
	tsStreamH264 = 0x1b
	tsStreamAAC  = 0x0f
	tsStreamID3  = 0x15
	tsStreamPriv = 0x06
	tsStreamHEVC = 0x24
)

// ParseTS demuxes an MPEG-TS byte slice.  Every packet is checked for its sync
// byte, every payload-bearing packet is checked for continuity, and PSI
// sections are parsed for the PAT/PMT.  A truncated final packet is reported
// at the first missing byte.
func ParseTS(data []byte, opts Options) (*Media, error) {
	media := &Media{Format: "ts"}
	psi := &tsPSI{pmtPIDs: make(map[uint16]bool), streams: make(map[uint16]tsStream)}
	assembler := newPESAssembler()
	cc := make(map[uint16]uint8)
	pos := 0
	for pos < len(data) {
		if pos+tsPacketSize > len(data) {
			if opts.TolerateTruncated {
				media.Cut = append(media.Cut, Range{Start: int64(len(data)), End: int64(pos + tsPacketSize)})
				break
			}
			return nil, fault.New(fault.StageDemux, "ts", fault.OpTruncated, int64(len(data)),
				"incomplete transport packet at end of input")
		}
		if data[pos] != tsSyncByte {
			return nil, fault.New(fault.StageDemux, "ts", fault.OpPacketHeader, int64(pos),
				"transport packet does not start with sync byte")
		}
		pkt := data[pos : pos+tsPacketSize]
		if err := parseTSPacket(pkt, int64(pos), psi, assembler, cc, opts); err != nil {
			return nil, err
		}
		pos += tsPacketSize
	}
	if err := assembler.flushAll(); err != nil {
		return nil, err
	}
	if psi.pmtPID == 0 {
		return nil, fault.New(fault.StageDemux, "ts", fault.OpIndexIncomplete, 0,
			"stream has no PAT/PMT program information")
	}
	media.Tracks = assembler.tracks(psi)
	events, err := assembler.events(media.Tracks)
	if err != nil {
		return nil, err
	}
	media.Events = events
	if len(media.Cut) > 0 {
		kept := media.Events[:0]
		for _, e := range media.Events {
			if e.SourceOffset < media.Cut[0].Start {
				kept = append(kept, e)
			}
		}
		media.Events = kept
	}
	for i := range media.Events {
		media.Events[i].Seq = i
	}
	return media, nil
}

type tsStream struct {
	pid      uint16
	streamID byte
	typ      byte
	kind     track.Kind
	codec    string
}

type tsPSI struct {
	pmtPID  uint16
	pmtPIDs map[uint16]bool
	streams map[uint16]tsStream
}

func parseTSPacket(pkt []byte, off int64, psi *tsPSI, asm *pesAssembler, cc map[uint16]uint8, opts Options) error {
	pusi := pkt[1]&0x40 != 0
	pid := binary.BigEndian.Uint16(pkt[1:]) & 0x1fff
	afc := (pkt[3] >> 4) & 3
	hasPayload := afc&1 != 0
	payloadStart := 4
	if afc&2 != 0 {
		afLen := int(pkt[4])
		payloadStart = 5 + afLen
		if payloadStart > tsPacketSize {
			return fault.New(fault.StageDemux, "ts", fault.OpPacketHeader, off+4,
				"adaptation field length exceeds transport packet")
		}
		if !hasPayload {
			return nil // adaptation field only, no continuity update
		}
	}
	if !hasPayload {
		return nil
	}
	payload := pkt[payloadStart:]
	if len(payload) == 0 {
		return nil
	}
	counter := pkt[3] & 0x0f
	if prev, ok := cc[pid]; ok && (prev+1)&0x0f != counter {
		return fault.New(fault.StageDemux, "ts", fault.OpSequence, off,
			"continuity counter discontinuity on elementary stream")
	}
	cc[pid] = counter

	if pid == 0 || psi.pmtPIDs[pid] {
		if pusi && len(payload) > int(payload[0]) {
			section := payload[1+int(payload[0]):]
			return parsePSISection(psi, pid, section, off+int64(payloadStart)+1+int64(payload[0]))
		}
		return nil
	}
	stream, ok := psi.streams[pid]
	if !ok {
		return nil
	}
	asm.feed(stream, pusi, off+int64(payloadStart), payload)
	return nil
}

func parsePSISection(psi *tsPSI, pid uint16, section []byte, off int64) error {
	if len(section) < 3 {
		return nil
	}
	if section[0] == 0xff {
		return nil // stuffing
	}
	tableID := section[0]
	sectLen := int(binary.BigEndian.Uint16(section[1:]) & 0x0fff)
	if 3+sectLen > len(section) {
		return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)),
			"PSI section extends past transport packet")
	}
	body := section[3 : 3+sectLen]
	switch tableID {
	case 0x00: // PAT
		if len(body) < 5 {
			return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)), "PAT section truncated")
		}
		p := 5
		for p+4 <= len(body)-4 {
			program := binary.BigEndian.Uint16(body[p:])
			pmtPID := binary.BigEndian.Uint16(body[p+2:]) & 0x1fff
			p += 4
			if program == 0 {
				continue
			}
			if psi.pmtPID == 0 {
				psi.pmtPID = pmtPID
			}
			psi.pmtPIDs[pmtPID] = true
		}
	case 0x02: // PMT
		if len(body) < 9 {
			return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)), "PMT section truncated")
		}
		progInfoLen := int(binary.BigEndian.Uint16(body[7:]) & 0x0fff)
		p := 9 + progInfoLen
		for p+5 <= len(body)-4 {
			streamType := body[p]
			esPID := binary.BigEndian.Uint16(body[p+1:]) & 0x1fff
			esInfoLen := int(binary.BigEndian.Uint16(body[p+3:]) & 0x0fff)
			p += 5
			if p+esInfoLen > len(body) {
				return fault.New(fault.StageDemux, "ts", fault.OpIncompleteHeader, off+int64(len(section)),
					"PMT elementary stream descriptors truncated")
			}
			desc := body[p : p+esInfoLen]
			p += esInfoLen
			st := tsStream{pid: esPID, typ: streamType, streamID: streamType}
			st.kind, st.codec = classifyTSStream(streamType, desc)
			psi.streams[esPID] = st
		}
	}
	return nil
}

func classifyTSStream(streamType byte, desc []byte) (track.Kind, string) {
	switch streamType {
	case tsStreamH264:
		return track.KindVideo, "avc"
	case tsStreamHEVC:
		return track.KindVideo, "hevc"
	case tsStreamAAC:
		return track.KindAudio, "aac"
	case tsStreamID3:
		return track.KindMetadata, "id3"
	case tsStreamPriv:
		if hasDescriptor(desc, 0x59) || hasDescriptor(desc, 0x46) {
			return track.KindSubtitle, "subtitle"
		}
		return track.KindMetadata, "private"
	}
	return track.KindUnknown, "unknown"
}

func hasDescriptor(desc []byte, tag byte) bool {
	for p := 0; p+2 <= len(desc); {
		t := desc[p]
		l := int(desc[p+1])
		p += 2
		if p+l > len(desc) {
			return false
		}
		if t == tag {
			return true
		}
		p += l
	}
	return false
}

// pesAssembler buffers transport payloads per PID until a PES packet is
// complete (signalled by the next payload_unit_start_indicator or end of
// input).
type pesAssembler struct {
	buffers map[uint16]*pesBuffer
	order   []uint16
	pushed  map[uint16][]tsPES
}

type pesBuffer struct {
	stream tsStream
	data   []byte
	offset int64
}

type tsPES struct {
	stream  tsStream
	offset  int64
	payload []byte
	pts     uint64
	dts     uint64
	hasPTS  bool
	hasDTS  bool
}

func newPESAssembler() *pesAssembler {
	return &pesAssembler{buffers: make(map[uint16]*pesBuffer), pushed: make(map[uint16][]tsPES)}
}

func (a *pesAssembler) feed(stream tsStream, pusi bool, off int64, payload []byte) {
	if pusi {
		if buf, ok := a.buffers[stream.pid]; ok {
			a.complete(buf)
		}
		buf := &pesBuffer{stream: stream, offset: off}
		a.buffers[stream.pid] = buf
		if _, ok := a.pushed[stream.pid]; !ok {
			a.pushed[stream.pid] = nil
		}
	}
	buf, ok := a.buffers[stream.pid]
	if !ok {
		return
	}
	buf.data = append(buf.data, payload...)
}

func (a *pesAssembler) flushAll() error {
	for _, pid := range a.sortedPIDs() {
		buf := a.buffers[pid]
		if buf != nil {
			a.complete(buf)
			delete(a.buffers, pid)
		}
	}
	return nil
}

func (a *pesAssembler) sortedPIDs() []uint16 {
	pids := make([]uint16, 0, len(a.buffers))
	for pid := range a.buffers {
		pids = append(pids, pid)
	}
	for i := 0; i < len(pids); i++ {
		for j := i + 1; j < len(pids); j++ {
			if pids[j] < pids[i] {
				pids[i], pids[j] = pids[j], pids[i]
			}
		}
	}
	return pids
}

func (a *pesAssembler) complete(buf *pesBuffer) {
	if len(buf.data) < 9 || buf.data[0] != 0 || buf.data[1] != 0 || buf.data[2] != 1 {
		return
	}
	headerLen := int(buf.data[8])
	if 9+headerLen > len(buf.data) {
		return
	}
	flags := buf.data[7]
	pes := tsPES{stream: buf.stream, offset: buf.offset, payload: buf.data[9+headerLen:]}
	if flags&0x80 != 0 && headerLen >= 5 {
		pes.pts = readPTS(buf.data[9:14])
		pes.hasPTS = true
	}
	if flags&0x40 != 0 && headerLen >= 10 {
		pes.dts = readPTS(buf.data[14:19])
		pes.hasDTS = true
	}
	a.pushed[buf.stream.pid] = append(a.pushed[buf.stream.pid], pes)
}

func readPTS(b []byte) uint64 {
	v := uint64((b[0]>>1)&0x07) << 30
	v |= uint64(b[1]) << 22
	v |= uint64(b[2]>>1) << 15
	v |= uint64(b[3]) << 7
	v |= uint64(b[4] >> 1)
	return v
}

func (a *pesAssembler) tracks(psi *tsPSI) []Track {
	var out []Track
	pids := make([]uint16, 0, len(a.pushed))
	for pid := range a.pushed {
		pids = append(pids, pid)
	}
	sortUint16(pids)
	lastDTS := make(map[uint16]uint64)
	for _, pid := range pids {
		pesList := a.pushed[pid]
		if len(pesList) == 0 {
			continue
		}
		st := psi.streams[pid]
		tr := Track{ID: uint32(pid), Kind: st.kind, Timescale: tsTimescale, Codec: st.codec, Handler: tsHandler(st.kind)}
		for _, pes := range pesList {
			if !pes.hasPTS {
				continue
			}
			dts := pes.pts
			if pes.hasDTS {
				dts = pes.dts
			}
			var delta uint32
			if prev, ok := lastDTS[pid]; ok && dts >= prev {
				delta = uint32(dts - prev)
			}
			lastDTS[pid] = dts
			tr.Samples = append(tr.Samples, Sample{
				Offset: pes.offset,
				Size:   uint32(len(pes.payload)),
				Delta:  delta,
				DTS:    clock.New(int64(dts), tsTimescale),
				CTS:    clock.New(int64(pes.pts), tsTimescale),
				Data:   pes.payload,
			})
		}
		out = append(out, tr)
	}
	return out
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

// events derives ID3 and SEI events from demuxed TS samples.
func (a *pesAssembler) events(tracks []Track) ([]metadata.Event, error) {
	var out []metadata.Event
	for ti := range tracks {
		tr := &tracks[ti]
		switch {
		case tr.Kind == track.KindMetadata && tr.Codec == "id3":
			for si := range tr.Samples {
				s := &tr.Samples[si]
				if len(s.Data) < 10 || string(s.Data[:3]) != "ID3" {
					continue
				}
				out = append(out, metadata.Event{
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
				out = append(out, metadata.Event{
					Kind: metadata.KindSubtitle, TrackID: tr.ID, Time: s.CTS,
					Data: s.Data, SourceOffset: s.Offset,
				})
			}
		case tr.Kind == track.KindVideo && tr.Codec == "avc":
			evs := annexBSEIEvents(tr)
			out = append(out, evs...)
		}
	}
	return out, nil
}

// annexBSEIEvents finds user_data_unregistered SEI messages in Annex B video
// samples.
func annexBSEIEvents(tr *Track) []metadata.Event {
	var out []metadata.Event
	for si := range tr.Samples {
		s := &tr.Samples[si]
		for _, nal := range annexBNALUs(s.Data) {
			if len(nal.data) == 0 || nal.data[0]&0x1f != 6 {
				continue
			}
			for _, m := range seiMessages(nal.data[1:]) {
				if m.typ != 5 {
					continue
				}
				// TS samples are assembled from several packets, so the only
				// exact source position is the start of the PES packet.
				out = append(out, metadata.Event{
					Kind: metadata.KindSEI, TrackID: tr.ID, Time: s.CTS, Data: m.payload,
					SourceOffset: s.Offset,
				})
			}
		}
	}
	return out
}

type annexBNAL struct {
	off  int
	data []byte
}

func annexBNALUs(b []byte) []annexBNAL {
	var out []annexBNAL
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				_ = start
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
