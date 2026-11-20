package report

import (
	"sort"

	"tsinspect/internal/demux"
	"tsinspect/internal/ts"
)

var streamTypeNames = map[uint8]string{
	0x01: "mpeg1_video",
	0x02: "mpeg2_video",
	0x03: "mpeg1_audio",
	0x04: "mpeg2_audio",
	0x05: "private_section",
	0x06: "private_data",
	0x0f: "adts_aac_audio",
	0x10: "mpeg4_video",
	0x11: "latm_aac_audio",
	0x15: "metadata",
	0x1b: "h264_video",
	0x24: "h265_video",
	0x42: "cavs_video",
	0x81: "ac3_audio",
	0x82: "dts_audio",
	0x83: "truehd_audio",
	0x86: "scte35",
	0x87: "eac3_audio",
	0xea: "vc1_video",
}

// StreamTypeName returns a stable name, or "unknown" for unregistered types.
func StreamTypeName(t uint8) string {
	if n, ok := streamTypeNames[t]; ok {
		return n
	}
	return "unknown"
}

// Build assembles a summary from a completed session. reader may be nil when
// only table and counter data are available.
func Build(input string, s *demux.Session, r *ts.Reader) *Summary {
	out := &Summary{
		InputPath:       input,
		ProgramToPMTPID: map[int]int{},
		PCRPIDByProgram: map[int]int{},
	}
	if r != nil {
		out.PrefixOffset = r.PrefixOffset()
		out.Packets = r.Packets()
		out.PacketBytes = r.Packets() * int64(ts.PacketSize)
		out.TrailingBytes = r.TrailingBytes()
		out.TrailingPackets = r.TrailingPackets()
	}
	st := s.Tables.Stats()
	out.PATVersionChanges = st.PATVersionChanges
	out.PMTVersionChanges = st.PMTVersionChanges
	out.PATDuplicates = st.PATDuplicates
	out.PMTDuplicates = st.PMTDuplicates
	out.FutureSections = st.FutureSections
	out.PendingDiscards = st.PendingDiscards

	sec := s.SectionStats()
	out.Sections = sec.Sections
	out.SectionCRCErrors = sec.CRCErrors
	out.SectionLengthErrors = sec.LengthErrors
	out.SectionsDiscarded = sec.Discarded

	tot := s.Counter.Totals()
	out.ContinuityErrors = tot.ContinuityErrs
	out.Duplicates = tot.Duplicates
	out.Discontinuities = tot.Discontinuities
	out.TransportErrors = tot.TransportErrors
	out.LostPackets = tot.LostPackets

	if pat := s.Tables.PAT(); pat != nil {
		v := int(pat.Version)
		tsid := pat.TransportStreamID
		out.PATVersion = &v
		out.PATTransportStreamID = &tsid
	}

	for _, program := range s.Tables.Programs() {
		pid, _ := s.Tables.ProgramPMTPID(program)
		out.ProgramToPMTPID[int(program)] = int(pid)
	}

	byPID := make(map[uint16]demux.PIDStat)
	for _, p := range s.Counter.PIDStats() {
		byPID[p.PID] = p
	}
	psiPIDs := make(map[uint16]bool)
	psiPIDs[demux.PATPID] = true
	for _, pid := range s.Tables.PMTPIDs() {
		psiPIDs[pid] = true
	}
	pcrPIDs := make(map[uint16]bool)

	// Programs in numeric order; streams sorted by PID within each program.
	for _, program := range s.Tables.Programs() {
		pmtPID, _ := s.Tables.ProgramPMTPID(program)
		prog := Program{ProgramNumber: program, PMTPID: pmtPID, PMTVersion: -1}
		if pmt, ok := s.Tables.PMT(program); ok {
			prog.Active = true
			prog.PCRPID = pmt.PCRPID
			prog.PMTVersion = int(pmt.Version)
		}
		out.PCRPIDByProgram[int(program)] = int(prog.PCRPID)
		if prog.PCRPID != 0 && prog.PCRPID != 0x1fff {
			pcrPIDs[prog.PCRPID] = true
			if _, ok := byPID[prog.PCRPID]; ok {
				prog.PCRPresent = true
			}
		}
		if prog.Active {
			pmt, _ := s.Tables.PMT(program)
			for _, es := range pmt.Streams {
				sr := Stream{
					ProgramNumber: program,
					StreamType:    es.StreamType,
					StreamTypeHex: hexByte(es.StreamType),
					PID:           es.PID,
					Description:   StreamTypeName(es.StreamType),
				}
				if p, ok := byPID[es.PID]; ok {
					sr.PacketCount = p.Packets
					sr.ContinuityErrors = p.ContinuityErrs
				}
				prog.Streams = append(prog.Streams, sr)
				out.Streams = append(out.Streams, sr)
			}
			sort.Slice(prog.Streams, func(i, j int) bool { return prog.Streams[i].PID < prog.Streams[j].PID })
		}
		out.Programs = append(out.Programs, prog)
	}
	sort.Slice(out.Programs, func(i, j int) bool { return out.Programs[i].ProgramNumber < out.Programs[j].ProgramNumber })
	sort.Slice(out.Streams, func(i, j int) bool {
		if out.Streams[i].ProgramNumber != out.Streams[j].ProgramNumber {
			return out.Streams[i].ProgramNumber < out.Streams[j].ProgramNumber
		}
		return out.Streams[i].PID < out.Streams[j].PID
	})

	streamPIDs := make(map[uint16]string)
	for _, sr := range out.Streams {
		streamPIDs[sr.PID] = "stream"
	}
	for _, p := range s.Counter.PIDStats() {
		kind := "other"
		switch {
		case p.PID == demux.NullPID:
			kind = "null"
		case psiPIDs[p.PID]:
			kind = "psi"
		case pcrPIDs[p.PID]:
			kind = "pcr"
		case streamPIDs[p.PID] != "":
			kind = "stream"
		}
		out.PIDs = append(out.PIDs, PIDReport{
			PID:              p.PID,
			Packets:          p.Packets,
			PayloadPackets:   p.PayloadPackets,
			AdaptationOnly:   p.AdaptationOnly,
			ContinuityErrors: p.ContinuityErrs,
			Duplicates:       p.Duplicates,
			Discontinuities:  p.Discontinuities,
			TransportErrors:  p.TransportErrors,
			LostPackets:      p.LostPackets,
			Kind:             kind,
		})
	}
	sort.Slice(out.PIDs, func(i, j int) bool { return out.PIDs[i].PID < out.PIDs[j].PID })
	return out
}

func hexByte(b uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{'0', 'x', digits[b>>4], digits[b&0x0f]})
}
