package ts

// parsePAT extracts the first program's PMT PID from a PAT section.
// A PAT entry is: program_number(16) reserved(3) PMT_PID(13), so the
// 0x1F mask belongs to the high PID byte (data[off+2]).
func parsePAT(p *Parser, data []byte) {
	// table_id(1) header(2) tsid(2) version(1) section(1) last(1) = 8
	if len(data) < 8 {
		return
	}
	end := len(data) - 4 // drop CRC_32
	for off := 8; off+4 <= end; {
		programNum := uint16(data[off])<<8 | uint16(data[off+1])
		pid := uint16(data[off+2]&0x1F)<<8 | uint16(data[off+3])
		if programNum != 0 && pid != 0x1FFF {
			p.PMTPID = pid
			break
		}
		off += 4
	}
}

// parsePMT records elementary stream PIDs and their stream types.
func parsePMT(p *Parser, data []byte) {
	// 8-byte section header, then PCR_PID(13), program_info_length(12).
	if len(data) < 12 {
		return
	}
	pcrPID := uint16(data[8]&0x1F)<<8 | uint16(data[9])
	_ = pcrPID
	infoLen := int(data[10]&0x0F)<<8 | int(data[11])
	off := 12 + infoLen
	end := len(data) - 4
	p.PMTSeen = true
	for off+5 <= end {
		streamType := data[off]
		pid := uint16(data[off+1]&0x1F)<<8 | uint16(data[off+2])
		esInfoLen := int(data[off+3]&0x0F)<<8 | int(data[off+4])
		p.Elementary[pid] = streamType
		switch streamType {
		case StreamH264:
			p.videoPID = pid
		case StreamAACADTS, StreamAACLATM:
			p.audioPID = pid
		}
		off += 5 + esInfoLen
	}
}

// ValidatePSI returns an error if no PAT/PMT was observed on the stream.
func (p *Parser) ValidatePSI() error {
	if p.PMTPID == 0 {
		return errNoPAT()
	}
	if !p.PMTSeen {
		return errNoPMT(p.PMTPID)
	}
	return nil
}
