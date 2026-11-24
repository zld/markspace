package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"mediadag/internal/gen"
	"mediadag/internal/ingest"
)

// digestRTPInput hashes canonical RTP wire content plus reorder parameters.
func digestRTPInput(sample *gen.Sample, reorderWindow int) []byte {
	h := sha256.New()
	json.NewEncoder(h).Encode(map[string]any{
		"kind": "rtp-ingest", "reorder_window": reorderWindow,
	})
	var b [8]byte
	for _, ev := range sample.Events {
		if ev.Kind != ingest.KindRTP || ev.Packet == nil {
			continue
		}
		p := ev.Packet
		binary.BigEndian.PutUint16(b[0:2], p.SequenceNumber)
		binary.BigEndian.PutUint32(b[2:6], p.Timestamp)
		if p.Marker {
			b[6] = 1
		} else {
			b[6] = 0
		}
		h.Write(b[:7])
		binary.BigEndian.PutUint32(b[0:4], uint32(len(p.Payload)))
		h.Write(b[:4])
		h.Write(p.Payload)
	}
	return []byte(hex.EncodeToString(h.Sum(nil)))
}

// digestRTCPInput hashes the RTCP anchors, which the timeline node owns.
func digestRTCPInput(sample *gen.Sample) []byte {
	h := sha256.New()
	h.Write([]byte("rtcp-timeline"))
	var b [12]byte
	for _, ev := range sample.Events {
		if ev.Kind != ingest.KindRTCP || ev.SR == nil {
			continue
		}
		binary.BigEndian.PutUint64(b[0:8], ev.SR.NTPTime)
		binary.BigEndian.PutUint32(b[8:12], ev.SR.RTPTime)
		h.Write(b[:])
	}
	return []byte(hex.EncodeToString(h.Sum(nil)))
}

func ingestInput(sample *gen.Sample, reorderWindow int) []byte {
	return []byte(fmt.Sprintf(`{"node":"ingest","rtp":%q,"reorder_window":%d,"v":%q}`,
		digestRTPInput(sample, reorderWindow), reorderWindow, nodeVersion))
}

func timelineInput(sample *gen.Sample, opts pipelineOptions) []byte {
	return []byte(fmt.Sprintf(`{"node":"timeline","rtcp":%q,"strict":%t,"v":%q}`,
		digestRTCPInput(sample), opts.StrictNAL, nodeVersion))
}

func inspectInput(sample *gen.Sample, opts pipelineOptions) []byte {
	return []byte(fmt.Sprintf(`{"node":"inspect","digest":%q,"strict":%t,"v":%q}`,
		sample.Digest, opts.StrictNAL, nodeVersion))
}

func manifestInput(segmentMs, targetKbps float64) []byte {
	return []byte(fmt.Sprintf(`{"node":"manifest","segment_ms":%g,"target_kbps":%g,"v":%q}`,
		segmentMs, targetKbps, nodeVersion))
}
