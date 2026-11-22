package packet

import (
	"fmt"

	"github.com/pion/rtcp"
)

// RTCPInfo summarises one (possibly compound) RTCP record.
type RTCPInfo struct {
	Packets int    // number of RTCP packets in the record
	Types   []uint8 // packet types in order of appearance
	RawLen  int
}

// ParseRTCP validates and parses one RTCP record (a single packet or a
// compound packet). off is the byte offset within the input stream.
func ParseRTCP(data []byte, off int64) (*RTCPInfo, error) {
	if len(data) < 4 {
		return nil, &Error{Offset: off, Seq: -1, Reason: "rtcp header truncated"}
	}
	if v := data[0] >> 6; v != 2 {
		return nil, &Error{Offset: off, Seq: -1, Reason: fmt.Sprintf("unsupported RTCP version %d", v)}
	}
	pkts, err := rtcp.Unmarshal(data)
	if err != nil {
		return nil, &Error{Offset: off, Seq: -1, Reason: "rtcp: " + err.Error()}
	}
	info := &RTCPInfo{RawLen: len(data), Packets: len(pkts)}
	// Walk the compound packet headers to record packet types in order.
	for pos := 0; pos+4 <= len(data); {
		info.Types = append(info.Types, data[pos+1])
		words := int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4 * (words + 1)
	}
	return info, nil
}
