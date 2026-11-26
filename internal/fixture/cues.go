package fixture

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"continuity/internal/cue"
)

// BuildRTPWrapRecords builds five packets crossing both uint16 sequence and
// uint32 timestamp wraparound. It returns length-prefixed raw RTP records.
func BuildRTPWrapRecords(seed int64) ([]byte, error) {
	seqs := []uint16{65534, 65535, 0, 1, 2}
	startTS := math.MaxUint32 - 3600
	var out bytes.Buffer
	for i, seq := range seqs {
		ts := uint32(int64(startTS) + int64(i)*3600)
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           uint32(seed) | 0x1000,
				Marker:         i == len(seqs)-1,
			},
			Payload: []byte{byte(i), byte(seed), 0xaa, 0xbb},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
	}
	return out.Bytes(), nil
}

// BuildEMSGSwitchHint creates a tiny fMP4-compatible segment containing styp
// and one version-0 emsg source-switch hint.
func BuildEMSGSwitchHint() ([]byte, error) {
	record := cue.SwitchRecord{
		Type:                 "source_switch",
		NewSource:            "backup",
		RawAnchorNS:          4_000_000_000,
		ContinuousAnchorNS:   600_000_000,
		MainLastContinuousNS: 580_000_000,
		BackupFirstRawNS:     4_000_000_000,
	}
	msg, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	emsgBox := &mp4.Emsg{
		FullBox:               mp4.FullBox{Version: 0},
		SchemeIdUri:           "urn:pwf:source-switch",
		Value:                 "main-to-backup",
		Timescale:             1000,
		PresentationTimeDelta: 600,
		EventDuration:         400,
		Id:                    17,
		MessageData:           msg,
	}
	payload := bytes.NewBuffer(nil)
	if _, err := mp4.Marshal(payload, emsgBox, mp4.Context{}); err != nil {
		return nil, err
	}
	box := isoBox("emsg", payload.Bytes())
	styp := isoBox("styp", buildStypPayload())
	return append(styp, box...), nil
}

func buildStypPayload() []byte {
	out := []byte{'m', 's', 'd', 'h'} // major brand
	var minor [4]byte
	binary.BigEndian.PutUint32(minor[:], 0)
	out = append(out, minor[:]...)
	out = append(out, 'm', 's', 'd', 'h')
	return out
}

func isoBox(boxType string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:4], size)
	copy(header[4:8], boxType)
	return append(header[:], payload...)
}
