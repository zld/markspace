package ingest

import (
	"encoding/binary"

	"example.com/mediatimeline/internal/model"
)

// SEIColors extracts color declarations carried in user-data-unregistered SEI
// messages whose payload starts with the four-byte "nclx" marker. This is the
// bitstream's claim, independent of the container's colr box.
func SEIColors(msgs []SEIMessage) []model.ColorDeclaration {
	var out []model.ColorDeclaration
	for _, m := range msgs {
		if m.Type != 5 {
			continue
		}
		// Accept both a bare "nclx" payload and the T.35-wrapped form
		// (country code 0xB5, provider 0x0031, then "nclx").
		body := m.Payload
		if len(body) >= 3 && body[0] == 0xB5 && body[1] == 0x00 && body[2] == 0x31 {
			body = body[3:]
		}
		if len(body) < 10 || string(body[:4]) != "nclx" {
			continue
		}
		out = append(out, model.ColorDeclaration{
			Source:    model.ColorFromBitstream,
			Primaries: binary.BigEndian.Uint16(body[4:6]),
			Transfer:  binary.BigEndian.Uint16(body[6:8]),
			Matrix:    binary.BigEndian.Uint16(body[8:10]),
			FullRange: len(body) > 10 && body[10]&0x80 != 0,
		})
	}
	return out
}
