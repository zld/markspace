package ts

import (
	"github.com/example/tsrepair/internal/model"
)

// sectionAssembler collects PSI payload bytes until section_length is reached.
type sectionAssembler struct {
	buf     []byte
	remain  int // bytes still expected after the first payload
	started bool
}

// push returns a complete section or nil while more packets are needed.
func (a *sectionAssembler) push(p *Packet) (*psiSection, error) {
	payload := p.Payload
	if p.PayloadUnitStart {
		if len(payload) < 1 {
			return nil, model.New(model.StagePSI, model.SeverityFatal,
				"pointer field missing").Wrap(model.ErrPointerField).At(p.PID, p.ByteOffset)
		}
		pointer := int(payload[0])
		if pointer+1 > len(payload) {
			return nil, model.New(model.StagePSI, model.SeverityFatal,
				"pointer field extends beyond payload").
				Wrap(model.ErrPointerField).At(p.PID, p.ByteOffset)
		}
		a.buf = append(a.buf[:0], payload[1:1+pointer]...)
		rest := payload[1+pointer:]
		if len(rest) >= 3 {
			secLen := int(rest[1]&0x0F)<<8 | int(rest[2])
			a.remain = secLen + 3 - len(rest)
		}
		a.buf = append(a.buf, rest...)
		a.started = true
	} else {
		if !a.started {
			return nil, nil // continuation without a start: wait
		}
		take := len(payload)
		if take > a.remain {
			take = a.remain
		}
		a.buf = append(a.buf, payload[:take]...)
		a.remain -= take
	}
	if a.remain > 0 || len(a.buf) < 8 {
		return nil, nil
	}
	return decodeSectionHeader(a.buf, p)
}

func decodeSectionHeader(buf []byte, p *Packet) (*psiSection, error) {
	if len(buf) < 8 {
		return nil, model.New(model.StagePSI, model.SeverityFatal,
			"section shorter than 8 bytes").At(p.PID, p.ByteOffset)
	}
	return &psiSection{
		tableID: buf[0],
		version: (buf[5] >> 1) & 0x1F,
		current: buf[5]&0x01 != 0,
		section: buf,
	}, nil
}

func parsePAT(sec []byte) (progNum uint16, pmtPID uint16, err *model.StreamError) {
	if sec[0] != 0x00 || len(sec) < 12 {
		return 0, 0, model.New(model.StagePSI, model.SeverityFatal, "malformed PAT")
	}
	progNum = uint16(sec[8])<<8 | uint16(sec[9])
	pmtPID = (uint16(sec[10]&0x1F) << 8) | uint16(sec[11])
	if pmtPID == 0 || pmtPID >= PIDNull {
		return 0, 0, model.New(model.StagePSI, model.SeverityFatal,
			"PAT carries no usable PMT PID").Wrap(model.ErrConflictingMap)
	}
	return progNum, pmtPID, nil
}

func parsePMT(sec []byte) (pcrPID uint16, tracks map[uint16]TrackMap, err *model.StreamError) {
	if sec[0] != 0x02 || len(sec) < 16 {
		return 0, nil, model.New(model.StagePSI, model.SeverityFatal, "malformed PMT")
	}
	pcrPID = (uint16(sec[8]&0x1F) << 8) | uint16(sec[9])
	piLen := (int(sec[10]&0x0F) << 8) | int(sec[11])
	pos := 12 + piLen
	end := len(sec) - 4 // exclude CRC_32
	tracks = map[uint16]TrackMap{}
	for pos+5 <= end {
		st := StreamType(sec[pos])
		pid := (uint16(sec[pos+1]&0x1F) << 8) | uint16(sec[pos+2])
		esInfoLen := (int(sec[pos+3]&0x0F) << 8) | int(sec[pos+4])
		pos += 5 + esInfoLen
		if _, dup := tracks[pid]; dup {
			return 0, nil, model.New(model.StagePSI, model.SeverityFatal,
				"duplicate elementary PID in PMT").Wrap(model.ErrConflictingMap)
		}
		tracks[pid] = TrackMap{PID: pid, StreamType: st, Kind: kindOf(st)}
	}
	return pcrPID, tracks, nil
}

func kindOf(st StreamType) TrackKind {
	switch st {
	case StreamTypeVideo:
		return KindVideo
	case StreamTypeAudio:
		return KindAudio
	default:
		return KindUnknown
	}
}
