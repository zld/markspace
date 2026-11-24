package rtpx

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"media-archiver/internal/media"
)

// FrameExtension is the reusable 64-bit frame-count timestamp extension.
// It is carried in an RTP header extension so legacy RTP timestamps can be
// adapted into the archive's sample-frame timeline.
type FrameExtension struct {
	ExtensionID   uint8  `json:"extension_id"`
	RTPTimestamp  uint32 `json:"rtp_timestamp"`
	ExtendedFrame int64  `json:"extended_frame"`
	TimeNS        int64  `json:"time_ns"`
}

// Adapter parses RTP packets and their frame-count header extension.
type Adapter struct {
	Timebase    media.Timebase
	ExtensionID uint8
}

// ParsePacket unmarshals an RTP packet with pion/rtp and decodes extension ID.
func (a Adapter) ParsePacket(packet []byte) (FrameExtension, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(packet); err != nil {
		return FrameExtension{}, media.NewError(media.CodeInvalidConfig, "", -1, "unmarshal RTP packet", err)
	}
	raw := pkt.GetExtension(a.ExtensionID)
	if len(raw) != 8 {
		return FrameExtension{}, media.NewError(media.CodeInvalidConfig, "", -1,
			fmt.Sprintf("RTP extension %d must contain 8-byte frame count", a.ExtensionID), nil)
	}
	frame := int64(binary.BigEndian.Uint64(raw))
	return FrameExtension{
		ExtensionID:   a.ExtensionID,
		RTPTimestamp:  pkt.Timestamp,
		ExtendedFrame: frame,
		TimeNS:        a.Timebase.FramesToNS(frame),
	}, nil
}

// PacketWithFrameExtension is a small helper for fixtures and adapter tests.
func PacketWithFrameExtension(clockRate uint32, rtpTimestamp uint32, extensionID uint8, frame uint64) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 1,
			Timestamp:      rtpTimestamp,
			SSRC:           0x171717,
		},
		Payload: []byte("pcm"),
	}
	ext := make([]byte, 8)
	binary.BigEndian.PutUint64(ext, frame)
	if err := pkt.Header.SetExtension(extensionID, ext); err != nil {
		return nil, err
	}
	return pkt.Marshal()
}
