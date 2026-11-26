package ts

import (
	"encoding/binary"
	"fmt"
	"io"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// NAL / start-code constants.
const (
	startCodePrefix3 = 0x000001
	startCodePrefix4 = 0x00000001

	// H.264 NAL unit types
	naluSlice = 1
	naluIDR   = 5
	naluSEI   = 6
	naluSPS   = 7
	naluPPS   = 8
	naluAUD   = 9
	// H.265 NAL unit types
	hevcBLAWLP   = 16
	hevcIDRWRADL = 19
	hevcIDRNLP   = 20
	hevcCRA      = 21
)

// Sample is one access unit (PES payload) with its raw byte offset.
type Sample struct {
	TrackID  uint32
	PTS      *clock.Time // nil when absent
	DTS      *clock.Time
	Keyframe bool
	AUOffset int64 // raw offset of the first byte of the access unit
	AUBytes  []byte
	// NALUnitTypes are the NAL unit types found in this AU (video only).
	NALUnitTypes []uint8
	// AUStartCodeLen is the length of the leading start code, when present.
	AUStartCodeLen int
	// PayloadRanges are the raw byte ranges, in file order, that hold AUBytes.
	// A PES spanning several transport packets is not contiguous in the file,
	// so verifying content requires these exact ranges.
	PayloadRanges []ByteSpan

	hasParameterSets bool
}

// ByteSpan is one contiguous raw byte extent in the input file.
type ByteSpan struct {
	Offset int64
	Length int64
}

// Track is one demultiplexed elementary stream.
type Track struct {
	TrackID    uint32 // stable id derived from PID order starting at 1
	PID        uint16
	StreamType uint8
	Language   string
	Kind       string // "video" or "audio"
	Timescale  uint32 // 90000 for TS PTS/DTS
	Samples    []Sample
	// TruncatedSamples counts trailing bytes that never formed a full PES.
	TruncatedSamples int
}

// Demux is the parsed transport stream.
type Demux struct {
	Path       string
	Program    uint16
	PCRPID     uint16
	Tracks     []*Track
	Packets    int64
	PSIPackets int64
	LastOffset int64
	// Faults holds recoverable defects encountered while demultiplexing.
	Faults []*fault.Error
}

// AddFault records a recoverable defect. Non-recoverable faults are returned
// by DemuxFile and stop parsing.
func (d *Demux) AddFault(err error) {
	if err == nil {
		return
	}
	if fe, ok := fault.As(err); ok {
		d.Faults = append(d.Faults, fe)
	}
}

// DemuxFile parses an entire MPEG-TS file. The whole file is scanned; samples
// are retained, so callers should size-limit the input before calling.
func DemuxFile(r io.Reader, path string) (*Demux, error) {
	pr := NewPacketReader(r, path)
	d := &Demux{Path: path}
	patAsm := NewSectionAssembler(path, 0x0000)
	pmtAsm := map[uint16]*SectionAssembler{}
	pmtPID := uint16(0)
	esByPID := map[uint16]*Track{}
	var trackOrder []*Track
	pesBuf := map[uint16]*pesAccumulator{}
	contSeen := map[uint16]uint8{}
	var packetsParsed int

	for {
		p, err := pr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if fe, ok := fault.As(err); ok && fe.Kind == fault.KindTruncate {
				d.AddFault(err)
				break
			}
			return nil, err
		}
		d.Packets++
		packetsParsed++
		// Continuity check on PIDs carrying PES. A jump means media was
		// lost; the affected layer must be stopped rather than guessed.
		if p.HasPayload {
			if prev, ok := contSeen[p.PID]; ok && p.Continuity != 0 {
				if (prev+1)&0x0F != p.Continuity && p.PID != 0x1FFF {
					f := fault.New(fault.KindCorrupt, path, p.Offset,
						fmt.Sprintf("continuity jump on pid %d: %d -> %d", p.PID, prev, p.Continuity)).
						Recover(false)
					if tr, ok := esByPID[p.PID]; ok {
						f.WithTrack(tr.TrackID, fmt.Sprint(tr.PID))
					}
					return nil, f
				}
			}
			contSeen[p.PID] = p.Continuity
		}
		switch {
		case p.PID == 0x0000:
			d.PSIPackets++
			secs, err := patAsm.Push(p)
			if err != nil {
				return nil, err
			}
			for _, sec := range secs {
				entries, err := ParsePAT(sec, path, p.Offset)
				if err != nil {
					return nil, err
				}
				if len(entries) > 0 {
					d.Program = entries[0].Program
					newPID := entries[0].PID
					if newPID != pmtPID {
						pmtPID = newPID
						pmtAsm[pmtPID] = NewSectionAssembler(path, pmtPID)
					}
				}
			}
		case p.PID == pmtPID && pmtPID != 0:
			d.PSIPackets++
			secs, err := pmtAsm[pmtPID].Push(p)
			if err != nil {
				return nil, err
			}
			for _, sec := range secs {
				pmt, err := ParsePMT(sec, path, p.Offset)
				if err != nil {
					return nil, err
				}
				if len(pmt.Streams) > 0 {
					d.Program, d.PCRPID = pmt.Program, pmt.PCRPID
					for _, es := range pmt.Streams {
						kind, ok := streamKind(es.StreamType)
						if !ok {
							// Ignore data streams (SCTE-35, subtitles) but never
							// invent media tracks for codecs we cannot copy.
							continue
						}
						if _, exists := esByPID[es.PID]; exists {
							continue
						}
						tr := &Track{
							TrackID:    uint32(len(trackOrder) + 1),
							PID:        es.PID,
							StreamType: es.StreamType,
							Language:   es.Language,
							Kind:       kind,
							Timescale:  90000,
						}
						esByPID[es.PID] = tr
						trackOrder = append(trackOrder, tr)
					}
				}
			}
		default:
			tr, ok := esByPID[p.PID]
			if !ok {
				continue
			}
			if !p.HasPayload {
				continue
			}
			acc := pesBuf[p.PID]
			if acc == nil {
				acc = &pesAccumulator{}
				pesBuf[p.PID] = acc
			}
			samples, err := acc.Push(tr, p, path)
			if err != nil {
				return nil, err
			}
			tr.Samples = append(tr.Samples, samples...)
		}
	}
	// Flush any pending PES that declared data but never received all of it.
	for pid, acc := range pesBuf {
		tr := esByPID[pid]
		if tr == nil || acc == nil || acc.Empty() {
			continue
		}
		if acc.Complete() {
			if s, ok, err := acc.Flush(tr, path); err != nil {
				return nil, err
			} else if ok {
				tr.Samples = append(tr.Samples, s)
			}
		} else {
			tr.TruncatedSamples++
			d.AddFault(fault.New(fault.KindTruncate, path, acc.Offset(),
				fmt.Sprintf("PES on pid %d ends before its declared length", pid)).
				WithTrack(tr.TrackID, fmt.Sprint(pid)).Recover(true))
		}
	}
	if pmtPID == 0 && len(trackOrder) == 0 {
		return nil, fault.New(fault.KindMissing, path, 0, "no PAT/PMT found in transport stream").Recover(false)
	}
	for _, tr := range trackOrder {
		if tr.Kind == "video" {
			annotateKeyframes(tr)
		}
	}
	d.Tracks = trackOrder
	d.LastOffset = pr.Offset()
	d.Packets = int64(packetsParsed)
	return d, nil
}

func streamKind(st uint8) (string, bool) {
	switch st {
	case StreamTypeH264, StreamTypeH265:
		return "video", true
	case StreamTypeAAC, StreamTypeMP3:
		return "audio", true
	default:
		return "", false
	}
}

// pesAccumulator gathers PES payload across packets for one PID.
type pesAccumulator struct {
	buf       []byte
	chunks    []ByteSpan
	startOff  int64
	declared  int
	headerLen int
	hasHeader bool
	pts, dts  *clock.Time
	started   bool
}

func (a *pesAccumulator) Empty() bool   { return a.buf == nil || len(a.buf) == 0 }
func (a *pesAccumulator) Offset() int64 { return a.startOff }
func (a *pesAccumulator) Complete() bool {
	return a.hasHeader && a.declared > 0 && len(a.buf) >= a.declared
}

// Push appends a packet payload and emits a completed sample, if any.
func (a *pesAccumulator) Push(tr *Track, p *Packet, path string) ([]Sample, error) {
	var out []Sample
	if p.PUSI {
		if a.started {
			if s, ok, err := a.Flush(tr, path); err != nil {
				return nil, err
			} else if ok {
				out = append(out, s)
			} else {
				tr.TruncatedSamples++
			}
		}
		a.reset()
		a.started = true
		if len(p.Payload) < 3 {
			return out, nil
		}
		a.startOff = p.PayloadOffset
		a.buf = append(a.buf, p.Payload...)
		a.chunks = append(a.chunks, ByteSpan{Offset: p.PayloadOffset, Length: int64(len(p.Payload))})
	} else {
		if !a.started {
			return out, nil
		}
		a.buf = append(a.buf, p.Payload...)
		a.chunks = append(a.chunks, ByteSpan{Offset: p.PayloadOffset, Length: int64(len(p.Payload))})
	}
	if !a.hasHeader && len(a.buf) >= 9 {
		if a.buf[0] != 0 || a.buf[1] != 0 || a.buf[2] != 1 {
			return nil, fault.New(fault.KindCorrupt, path, a.startOff,
				"PES packet does not start with 0x000001").Recover(false)
		}
		strmID := a.buf[3]
		if strmID == 0xBE || strmID == 0xBF || (strmID >= 0xF0 && strmID <= 0xF2) || strmID == 0xFF {
			// padding/private streams: not media, discarding is not a guess.
			a.reset()
			return out, nil
		}
		plen := int(binary.BigEndian.Uint16(a.buf[4:6]))
		flags := a.buf[7]
		hl := int(a.buf[8])
		if 9+hl > len(a.buf) {
			// header spans packets; wait for more data
			return out, nil
		}
		a.hasHeader = true
		a.headerLen = 9 + hl
		if plen == 0 {
			a.declared = 0
		} else {
			a.declared = 6 + plen
		}
		if flags&0x80 != 0 && hl >= 5 {
			ptsDts := a.buf[9 : 9+hl]
			if t, ok := decodeTimestamp(ptsDts, 0x02); ok {
				a.pts = &t
			}
			if flags&0x40 != 0 && len(ptsDts) >= 10 {
				if t, ok := decodeTimestamp(ptsDts, 0x03); ok {
					a.dts = &t
				}
			}
		}
	}
	return out, nil
}

func (a *pesAccumulator) reset() {
	a.buf = nil
	a.chunks = nil
	a.hasHeader = false
	a.declared = 0
	a.headerLen = 0
	a.pts, a.dts = nil, nil
	a.startOff = 0
	a.started = false
}

// Flush returns the assembled sample when it is complete.
func (a *pesAccumulator) Flush(tr *Track, path string) (Sample, bool, error) {
	if !a.hasHeader || !a.Complete() {
		a.reset()
		return Sample{}, false, nil
	}
	end := a.declared
	if a.declared == 0 {
		end = len(a.buf)
	}
	data := a.buf[a.headerLen:end]
	s := Sample{
		TrackID:       tr.TrackID,
		PTS:           a.pts,
		DTS:           a.dts,
		AUOffset:      a.startOff + int64(a.headerLen),
		AUBytes:       append([]byte(nil), data...),
		PayloadRanges: sliceSpans(a.chunks, int64(a.headerLen), int64(end)),
	}
	if tr.Kind == "video" {
		s.NALUnitTypes, s.AUStartCodeLen = scanNALTypes(data)
		s.Keyframe = isVideoKeyframe(s.NALUnitTypes)
	}
	a.reset()
	return s, true, nil
}

// sliceSpans projects the byte interval [from,to) of the concatenated chunks
// onto the chunks' raw file ranges.
func sliceSpans(chunks []ByteSpan, from, to int64) []ByteSpan {
	var out []ByteSpan
	var pos int64
	for _, c := range chunks {
		chunkEnd := pos + c.Length
		if chunkEnd <= from {
			pos = chunkEnd
			continue
		}
		if pos >= to {
			break
		}
		start := maxI64(from, pos)
		stop := minI64(to, chunkEnd)
		if stop > start {
			out = append(out, ByteSpan{Offset: c.Offset + (start - pos), Length: stop - start})
		}
		pos = chunkEnd
	}
	return out
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// decodeTimestamp decodes a 33-bit PTS/DTS from a PES header. The expected
// leading nibble (2 for PTS, 3 for DTS) guards against malformed headers.
func decodeTimestamp(b []byte, wantNibble byte) (clock.Time, bool) {
	if len(b) < 5 {
		return clock.Time{}, false
	}
	if b[0]>>4 != wantNibble {
		return clock.Time{}, false
	}
	ticks := int64(b[0]&0x0E)<<29 |
		int64(b[1])<<22 |
		int64(b[2]&0xFE)<<14 |
		int64(b[3])<<7 |
		int64(b[4])>>1
	return clock.Time{Ticks: ticks, Timescale: 90000}, true
}

// scanNALTypes lists NAL unit types in an Annex B access unit and reports the
// length of the leading start code (3 or 4 bytes; 0 if absent).
func scanNALTypes(data []byte) ([]uint8, int) {
	var types []uint8
	leading := 0
	i := 0
	first := true
	for i+3 <= len(data) {
		var prefix int
		switch {
		case i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1:
			prefix = 4
		case data[i] == 0 && data[i+1] == 0 && data[i+2] == 1:
			prefix = 3
		}
		if prefix == 0 {
			i++
			continue
		}
		if first {
			leading = prefix
			first = false
		}
		nalStart := i + prefix
		if nalStart >= len(data) {
			break
		}
		types = append(types, nalType(data[nalStart]))
		i = nalStart + 1
	}
	return types, leading
}

// nalType returns the H.264 NAL type, or the H.265 type shifted into a
// disjoint range so a single classifier can distinguish both.
func nalType(b byte) uint8 {
	if b&0x80 != 0 {
		// H.265 forbidden_zero_bit=0, type in bits 1..6
		return 0x80 | ((b >> 1) & 0x3F)
	}
	return b & 0x1F
}

func isVideoKeyframe(types []uint8) bool {
	for _, t := range types {
		switch t {
		case naluIDR:
			return true
		case hevcBLAWLP, hevcIDRWRADL, hevcIDRNLP, hevcCRA:
			return true
		}
	}
	return false
}

// annotateKeyframes records, for each keyframe, whether a preceding SPS/PPS
// was present so the remuxer can emit a self-contained init segment.
func annotateKeyframes(tr *Track) {
	for i := range tr.Samples {
		s := &tr.Samples[i]
		if !s.Keyframe {
			continue
		}
		hasSPS := false
		for _, t := range s.NALUnitTypes {
			if t == naluSPS || t == naluPPS || t == (0x80|33) || t == (0x80|34) {
				hasSPS = true
				break
			}
		}
		s.hasParameterSets = hasSPS
	}
}

// Duration returns the exact span covered by sample DTS/PTS values, if any.
func (tr *Track) Duration() (clock.Time, bool) {
	var first, last int64
	found := false
	for _, s := range tr.Samples {
		t := s.DTS
		if t == nil {
			t = s.PTS
		}
		if t == nil {
			continue
		}
		if !found || t.Ticks < first {
			first = t.Ticks
		}
		if !found || t.Ticks > last {
			last = t.Ticks
		}
		found = true
	}
	if !found {
		return clock.Time{}, false
	}
	return clock.Time{Ticks: last - first, Timescale: tr.Timescale}, true
}
