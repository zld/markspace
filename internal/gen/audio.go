package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediarun/internal/fault"
	rtpparse "mediarun/internal/rtp"
)

// buildAudio produces the length-prefixed RTP sidecar. It returns the bytes and
// the stats needed by the pipeline and its tests.
func buildAudio(opts Options, totalFrames int, inject map[string]bool) ([]byte, rtpparse.Stats, error) {
	const (
		clockRate = 48000
		payloadT  = 111
		ssrc      = 0x0BADCAFE
		packetMs  = 20
	)
	unitsPerFrame := 2
	packets := totalFrames * unitsPerFrame

	base := uint32(0xFFFFF000)
	if !inject["seqwrap"] {
		base = 1000
	}
	stats := rtpparse.Stats{
		BaseRTP:   base,
		ClockRate: clockRate,
		Payload:   payloadT,
		SSRC:      ssrc,
		Packets:   packets,
	}

	type pkt struct {
		seq uint16
		ts  uint32
		raw []byte
	}
	seqBase := uint16(65500)
	tsBase := base
	if inject["seqwrap"] {
		seqBase = 65500
	}
	startDelayTicks := uint32(opts.AudioStartLateNs * int64(clockRate) / 1e9)
	offsetTicks := uint32(opts.AudioOffsetNs * int64(clockRate) / 1e9)
	list := make([]pkt, 0, packets)
	drop := map[int]bool{}
	dupe := map[int]bool{}
	if inject["gap"] {
		// A recoverable three packet hole and an unrecoverable twelve packet
		// hole. Both are sealed by recovery, so the timeline keeps its
		// duration; only the long one marks the fragment incomplete.
		for i := 20; i < 23; i++ {
			drop[i] = true
		}
		for i := 48; i < 60; i++ {
			drop[i] = true
		}
	}
	if inject["overflow"] {
		// Drop one packet without a gap so the reversed run below is what
		// forces the buffer past its window.
		drop[24] = true
	}
	if inject["dupe"] {
		for i := 10; i < 12; i++ {
			dupe[i] = true
		}
	}
	for i := 0; i < packets; i++ {
		seq := seqBase + uint16(i)
		ts := tsBase + offsetTicks + startDelayTicks + uint32(i*packetMs*clockRate/1000)
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
				PayloadType:    payloadT,
				Marker:         i%unitsPerFrame == unitsPerFrame-1,
			},
			Payload: makeAudioPayload(i),
		}
		raw, err := p.Marshal()
		if err != nil {
			return nil, stats, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "marshal rtp %d", i)
		}
		if drop[i] {
			continue
		}
		list = append(list, pkt{seq: seq, ts: ts, raw: raw})
		if dupe[i] {
			list = append(list, pkt{seq: seq, ts: ts, raw: raw})
		}
	}
	if inject["overflow"] {
		// Reverse a long run so the jitter buffer must hold more than its
		// window, and the excess is reported rather than silently dropped.
		lo, hi := 25, len(list)
		if hi > len(list) {
			hi = len(list)
		}
		if lo < hi {
			for i, j := lo, hi-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
	} else if inject["gap"] || inject["dupe"] || inject["seqwrap"] {
		// Keep wire order natural; drops and duplicates alone exercise the
		// buffer's reordering. No shuffle needed.
	} else {
		// Introduce mild reordering in every healthy fixture so ordering is
		// always exercised, not only in fault runs.
		for i := 5; i+1 < len(list) && i < 15; i += 4 {
			list[i], list[i+1] = list[i+1], list[i]
		}
	}

	var buf bytes.Buffer
	buf.WriteString("RTP1")
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(len(list)))
	buf.Write(tmp[:])
	for _, p := range list {
		binary.BigEndian.PutUint32(tmp[:], uint32(len(p.raw)))
		buf.Write(tmp[:])
		buf.Write(p.raw)
	}
	return buf.Bytes(), stats, nil
}

// makeAudioPayload synthesizes a deterministic Opus-shaped frame.
func makeAudioPayload(i int) []byte {
	p := make([]byte, 32)
	copy(p, "OPUS")
	binary.BigEndian.PutUint32(p[4:8], uint32(i))
	sum := sha256.Sum256([]byte(fmt.Sprintf("audio-%d", i)))
	copy(p[8:], sum[:16])
	for j := 24; j < len(p); j++ {
		p[j] = byte(i + j)
	}
	return p
}

func writeStats(path string, st rtpparse.Stats) error {
	if err := rtpparse.WriteStats(path, st); err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", path)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
