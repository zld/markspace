package samplegen

import (
	"bytes"
	"encoding/binary"
	"encoding/json"

	"github.com/pion/rtp"

	"assetpack/internal/adapter"
)

// BuildRTPStream creates a length-framed local RTP/v1 fixture.
// fault is one of "", "rtp-duplicate", or "rtp-missing".
func BuildRTPStream(fault string) ([]byte, error) {
	desc := adapter.RTPDescriptor{
		APIVersion: 1,
		Tracks: []adapter.RTPTrackDesc{
			{
				PayloadType: 96, ID: "rtp-video", Kind: "video",
				Codec: adapter.ID{Name: adapter.CodecRGBA8, Version: 1}, ClockRate: 90000,
				FrameDuration: 3750, Width: Width, Height: Height,
			},
			{
				PayloadType: 97, ID: "rtp-audio", Kind: "audio",
				Codec: adapter.ID{Name: adapter.CodecPCMS16, Version: 1}, ClockRate: 48000,
				FrameDuration: 960, SampleRate: 48000, Channels: 1,
			},
		},
	}
	rawDesc, err := json.Marshal(desc)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(adapter.RTPMagic)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(rawDesc)))
	out.Write(rawDesc)

	seq := uint16(4000)
	for i := 0; i < 6; i++ {
		if fault == "rtp-missing" && i == 3 {
			seq++ // skip exactly the next expected sequence number
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: seq,
				Timestamp: uint32(i * 3750), SSRC: 0x41535345, Marker: i == 5,
			},
			Payload: []byte{byte(i), 0, 0, 255},
		}
		if err := writeRTPPacket(&out, pkt); err != nil {
			return nil, err
		}
		seq++
		if fault == "rtp-duplicate" && i == 2 {
			// Repeat the exact previous sequence in the next frame slot.
			dup := *pkt
			dup.Timestamp += 3750
			if err := writeRTPPacket(&out, &dup); err != nil {
				return nil, err
			}
			i++
		}
	}
	return out.Bytes(), nil
}

func writeRTPPacket(out *bytes.Buffer, pkt *rtp.Packet) error {
	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(raw)))
	out.Write(lenBuf[:])
	out.Write(raw)
	return nil
}

// UnknownVersionContainer rewrites the reference descriptor to version 9.
func UnknownVersionContainer() ([]byte, error) {
	built, err := BuildContainer(1, false)
	if err != nil {
		return nil, err
	}
	return bytes.Replace(built.Data, []byte(`"api_version":1`), []byte(`"api_version":9`), 1), nil
}
