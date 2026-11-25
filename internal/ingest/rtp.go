package ingest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/pion/rtp"

	"mediavault/internal/timeline"
)

// RTPCapture is a project-generated flat binary capture: a repeating
// sequence of a 4-byte big-endian length prefix and one RTP packet. The
// framing lets the ingest layer report precise byte offsets when a packet
// is truncated mid-capture.
type RTPCapture struct {
	Source        string
	ClockRate     uint32
	Kind          timeline.TrackKind
	JumpThreshold uint64 // raw-tick excursion treated as a source switch
}

// RTPPacketRef retains the byte offset of each packet for traceability.
type RTPPacketRef struct {
	Offset int64
	Packet *rtp.Packet
	Raw    []byte
}

// ReadRTPFile streams an RTP capture file into parsed packets. A truncated
// length prefix or packet body yields a StageError tagged truncated-capture
// with the byte offset and the recoverable flag set (re-delivery can fix it).
func ReadRTPFile(path string, cap RTPCapture) ([]RTPPacketRef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var refs []RTPPacketRef
	var offset int64
	var lenBuf [4]byte
	for {
		n, err := io.ReadFull(f, lenBuf[:])
		if err == io.EOF {
			return refs, nil
		}
		if err == io.ErrUnexpectedEOF {
			return refs, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture,
				Offset: offset, Recoverable: true,
				Detail: fmt.Sprintf("length prefix truncated: got %d/4 bytes", n), Err: err,
			}
		}
		if err != nil {
			return refs, err
		}
		pktLen := binary.BigEndian.Uint32(lenBuf[:])
		if pktLen == 0 || pktLen > 1<<20 {
			return refs, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindIllegalBoxLength,
				Offset: offset, Recoverable: false,
				Detail: fmt.Sprintf("illegal RTP packet length %d", pktLen),
			}
		}
		raw := make([]byte, pktLen)
		pktOffset := offset + 4
		if _, err := io.ReadFull(f, raw); err != nil {
			return refs, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture,
				Offset: pktOffset, Recoverable: true,
				Detail: fmt.Sprintf("RTP packet body truncated: %v", err), Err: err,
			}
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return refs, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindTruncatedCapture,
				Offset: pktOffset, Recoverable: false,
				Detail: fmt.Sprintf("RTP unmarshal failed: %v", err), Err: err,
			}
		}
		refs = append(refs, RTPPacketRef{Offset: pktOffset, Packet: pkt, Raw: raw})
		offset = pktOffset + int64(pktLen)
	}
}

// IngestRTP linearizes parsed RTP packets through a track normalizer and
// returns the resulting samples together with per-packet offsets. Boundary
// jumps are returned to the caller (the repair layer) rather than repaired
// here so the correction remains explicit and traceable.
func IngestRTP(refs []RTPPacketRef, cap RTPCapture) ([]timeline.Sample, []*timeline.StageError, error) {
	if len(refs) == 0 {
		return nil, nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindMissingTrack,
			Recoverable: false, Detail: "RTP capture contains no packets",
		}
	}
	ssrc := refs[0].Packet.SSRC
	id := timeline.TrackID{Source: cap.Source, Index: 0, Kind: cap.Kind}
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: cap.Source, Clock: ssrc}, cap.ClockRate, cap.JumpThreshold)
	var samples []timeline.Sample
	var jumpErrs []*timeline.StageError
	for _, ref := range refs {
		ts, err := norm.PushRTP(ref.Packet)
		if err != nil {
			if se, ok := err.(*timeline.StageError); ok && se.Kind == timeline.KindBoundaryJump {
				se.Offset = ref.Offset
				jumpErrs = append(jumpErrs, se)
				// A source switch holds the timeline until repair decides;
				// later packets are not linearized against the jumped clock.
				break
			}
			return samples, norm.Errors(), err
		}
		if ts == nil {
			continue // duplicate timestamp already recorded on the normalizer
		}
		samples = append(samples, timeline.Sample{
			Track:   id,
			Order:   ts.Order,
			TS:      *ts,
			Payload: ref.Packet.Payload,
			Marker:  ref.Packet.Marker,
		})
	}
	return samples, append(jumpErrs, norm.Errors()...), nil
}
