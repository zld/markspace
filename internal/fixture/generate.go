// Package fixture generates the synthetic MP4/RTP inputs every other subsystem
// consumes. All data is produced in code; no external media is referenced.
package fixture

import (
	"encoding/binary"
	"hash/fnv"
	"os"
	"path/filepath"

	"avpipe/internal/model"
	"avpipe/internal/mp4box"
	"github.com/pion/rtp"
)

// genTrack builds the RTP packet stream for one track and returns its raw
// byte content. It deterministically injects every boundary declared in the
// descriptor so repair detection has ground truth to be tested against.
func genTrack(td model.TrackDesc) ([]byte, error) {
	var (
		out      []byte
		ts       = uint64(td.StartTS) // unwrapped working timestamp
		sourceID = uint32(1)
		boundAt  = map[int]model.BoundarySpec{}
		truncate bool
	)
	for _, b := range td.Boundaries {
		boundAt[b.AfterSeq] = b
		if b.Type == model.BTruncate {
			truncate = true
		}
	}

	for seq := 0; seq < td.Samples; seq++ {
		if b, ok := boundAt[seq-1]; ok {
			switch b.Type {
			case model.BWrap:
				// Continue the unwrapped logical clock; the high StartTS makes
				// the uint32 value cross zero exactly at this boundary.
				ts = uint64(td.StartTS) + uint64(td.StepTicks)*uint64(seq)
			case model.BSwitch:
				// New source: SSRC changes and the clock resets low.
				sourceID++
				ts = uint64(td.StartTS)
			case model.BIllegal:
				// Undecidable backward jump on the same source: large enough
				// that it cannot be explained by one 32-bit wraparound.
				ts = uint64(td.StartTS) + uint64(td.StepTicks)
			}
		}
		raw := uint32(ts)
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    uint8(96 + td.ID),
				SequenceNumber: uint16(seq),
				Timestamp:      raw,
				SSRC:           uint32(0xA000 + sourceID),
				Marker:         seq == td.Samples-1,
			},
			Payload: []byte{byte(td.ID), byte(seq >> 8), byte(seq)},
		}
		rawPkt, err := pkt.Marshal()
		if err != nil {
			return nil, model.NewError(model.CodeIO, false, "rtp marshal: %v", err)
		}
		// Length-prefixed frame: uint32 big-endian length then packet bytes.
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(rawPkt)))
		out = append(out, hdr...)
		out = append(out, rawPkt...)
		ts += uint64(td.StepTicks)
	}

	if truncate && len(out) > 2 {
		// Drop the final packet's last payload byte, leaving a short tail block.
		out = out[:len(out)-1]
	}
	return out, nil
}

// WriteCase generates every track file and the MP4 index inside dir.
func WriteCase(dir string, desc model.CaseDesc) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.NewError(model.CodeIO, false, "mkdir: %v", err)
	}
	var totalSamples, totalTicks uint32
	for _, td := range desc.Tracks {
		data, err := genTrack(td)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, model.RTPPath(td.ID))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return model.NewError(model.CodeIO, false, "write rtp: %v", err)
		}
		totalSamples += uint32(td.Samples)
		totalTicks += uint32(td.Samples) * td.StepTicks
	}
	// mvhd timescale uses the first track clock; duration sums sample ticks.
	ts := desc.Tracks[0].ClockRate
	mp4Path := filepath.Join(dir, model.IndexMP4)
	f, err := os.Create(mp4Path)
	if err != nil {
		return model.NewError(model.CodeIO, false, "create mp4: %v", err)
	}
	defer f.Close()
	if err := mp4box.Write(f, desc, ts, totalTicks); err != nil {
		return model.NewError(model.CodeIO, false, "write mp4: %v", err)
	}
	return nil
}

// Digest returns a stable 64-bit FNV-1a hash of raw bytes, hex encoded. It is
// used for input digests and output hashing.
func Digest(data []byte) string {
	h := fnv.New64a()
	h.Write(data)
	return uintToHex(h.Sum64())
}

func uintToHex(v uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[v&0xf]
		v >>= 4
	}
	return string(out)
}
