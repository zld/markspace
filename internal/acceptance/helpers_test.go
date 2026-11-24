package acceptance

import (
	"os"

	"github.com/pion/rtp"
)

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func rtpDatagram(seq uint16, payload []byte) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq, SSRC: 1,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}
