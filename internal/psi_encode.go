package kernel

import "sort"

// EncodePATSection builds a single-program PAT with correct MPEG-2 CRC.
func EncodePATSection(version uint8, pmtPID uint16) []byte {
	out := []byte{0x00, 0xB0, 0x00}
	out = append(out, 0x00, 0x01) // transport_stream_id
	out = append(out, 0xC0|((version&0x1f)<<1)|0x01)
	out = append(out, 0x00, 0x00)
	out = append(out, 0x00, 0x01) // program_number
	out = append(out, 0xE0|byte(pmtPID>>8), byte(pmtPID))
	return finalizeSection(out)
}

// PMTStream pairs an elementary PID with its MPEG stream type.
type PMTStream struct {
	PID        uint16
	StreamType uint8
}

// EncodePMTSection builds a one-section PMT with correct MPEG-2 CRC.
func EncodePMTSection(version uint8, pcrPID uint16, streams []PMTStream) []byte {
	out := []byte{0x02, 0xB0, 0x00}
	out = append(out, 0x00, 0x01) // program_number
	out = append(out, 0xC0|((version&0x1f)<<1)|0x01)
	out = append(out, 0x00, 0x00)
	out = append(out, 0xE0|byte(pcrPID>>8), byte(pcrPID))
	out = append(out, 0xF0, 0x00) // program_info_length=0

	sorted := append([]PMTStream(nil), streams...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PID < sorted[j].PID })
	for _, s := range sorted {
		out = append(out, s.StreamType)
		out = append(out, 0xE0|byte(s.PID>>8), byte(s.PID))
		out = append(out, 0xF0, 0x00) // ES_info_length=0
	}
	return finalizeSection(out)
}

// TSPayloadWithPointer prepends a zero pointer field for PUSI PSI packets.
func TSPayloadWithPointer(section []byte) []byte {
	out := make([]byte, 0, len(section)+1)
	out = append(out, 0x00)
	return append(out, section...)
}

func finalizeSection(out []byte) []byte {
	sectionLength := len(out) - 3 + 4 // CRC32 follows
	out[1] = 0xB0 | byte((sectionLength>>8)&0x0f)
	out[2] = byte(sectionLength)
	crc := mpegCRC32(out)
	out = append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	return out
}

func mpegCRC32(data []byte) uint32 {
	const poly uint32 = 0x04C11DB7
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ poly
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
