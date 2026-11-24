package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// CaptureMagic and CaptureVersion head every RTP capture file.
var CaptureMagic = [4]byte{'A', 'R', 'T', 'P'}

const (
	captureHeaderLen = 12
	captureVersion   = 1
	// burstLossCount packets are removed in burst-loss mode. They fall
	// outside the default window, so they must surface as real gaps.
	burstLossCount = 5
	// duplicateCount retransmissions are injected in duplicate-order mode.
	duplicateCount = 2
)

// CaptureHeader documents the 12-byte wire header preceding the
// length-prefixed RTP datagrams.
type CaptureHeader struct {
	Magic      [4]byte // 0:4
	Version    uint8   // 4
	TrackID    uint32  // 5:9
	SegmentSeq uint16  // 9:11
	Reserved   uint8   // 11
}

// captureRecord is one emitted RTP datagram plus delivery annotations.
type captureRecord struct {
	pkt  *rtp.Packet
	drop bool // removed by burst loss
	dup  bool // retransmission copy
}

// writeCapture packetizes the segment frames into RTP datagrams,
// applies the scenario transport transform and writes the capture file.
func writeCapture(fx *Fixture, spec media.TrackSpec, seq uint32,
	frames []FrameSpec, cfg Config) error {

	records := packetize(spec, seq, frames)
	records = applyTransport(records, cfg, spec, seq)

	path := capturePath(fx, spec.ID, seq)
	f, err := os.Create(path)
	if err != nil {
		return merr.Wrap(err, "fixture", path, "cannot create capture")
	}
	defer f.Close()

	hdr := make([]byte, captureHeaderLen)
	copy(hdr[0:4], CaptureMagic[:])
	hdr[4] = captureVersion
	binary.BigEndian.PutUint32(hdr[5:9], spec.ID)
	binary.BigEndian.PutUint16(hdr[9:11], uint16(seq))
	hdr[11] = 0 // reserved
	if _, err := f.Write(hdr); err != nil {
		return merr.Wrap(err, "fixture", path, "header write failed")
	}

	lost := 0
	for _, rec := range records {
		if rec.drop {
			lost++
			continue
		}
		data, err := rec.pkt.Marshal()
		if err != nil {
			return merr.Wrap(err, "fixture", path, "rtp marshal failed")
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return merr.Wrap(err, "fixture", path, "length write failed")
		}
		if _, err := f.Write(data); err != nil {
			return merr.Wrap(err, "fixture", path, "packet write failed")
		}
	}
	if err := f.Sync(); err != nil {
		return merr.Wrap(err, "fixture", path, "sync failed")
	}
	return nil
}

// packetize turns deterministic frame bytes into ordered RTP packets.
// Each frame spans one or more packets whose payloads concatenate back
// to the original frame; sequence numbers and timestamps follow RFC 3550.
func packetize(spec media.TrackSpec, seq uint32, frames []FrameSpec) []captureRecord {
	var out []captureRecord
	var rtpSeq uint16
	ts := uint32(0)
	for fi, fs := range frames {
		raw := BuildFrame(fs)
		ts = uint32(fi) * uint32(media.ClockRate/FramesPerSegment)
		for off := 0; off < len(raw); {
			end := off + MTU
			if end > len(raw) {
				end = len(raw)
			}
			chunk := make([]byte, end-off)
			copy(chunk, raw[off:end])
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: rtpSeq,
					Timestamp:      ts,
					SSRC:           uint32(spec.ID) * 7919,
					Marker:         end == len(raw),
				},
				Payload: chunk,
			}
			out = append(out, captureRecord{pkt: pkt})
			rtpSeq++
			off = end
		}
	}
	return out
}

// applyTransport models the per-scenario delivery conditions.
func applyTransport(records []captureRecord, cfg Config,
	spec media.TrackSpec, seq uint32) []captureRecord {

	switch cfg.Scenario {
	case ScenarioBurstLoss:
		// Only the first track suffers a burst loss on each segment.
		if spec.ID != 1 {
			return records
		}
		start := len(records)/2 - burstLossCount/2
		for i := start; i < start+burstLossCount && i < len(records); i++ {
			records[i].drop = true
		}
		return records

	case ScenarioDuplicateOrder:
		// Deterministic adjacent swaps plus retransmitted duplicates.
		swapped := make([]captureRecord, len(records))
		copy(swapped, records)
		for i := 1; i+1 < len(swapped); i += 4 {
			swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
		}
		var withDup []captureRecord
		for i, rec := range swapped {
			withDup = append(withDup, rec)
			if i%5 == 3 && len(withDup) > 0 && i < duplicateCount*3 {
				cp := rec
				cp.dup = true
				withDup = append(withDup, cp)
			}
		}
		return withDup

	default:
		// valid / repaired / truncated: an intact, ordered capture.
		return records
	}
}

// writeSegment renders the fMP4 file and its sidecar metadata.
func writeSegment(fx *Fixture, spec media.TrackSpec, seq uint32,
	frames []FrameSpec, cfg Config) error {

	rawFrames := make([][]byte, len(frames))
	totalBytes := 0
	for i, fs := range frames {
		rawFrames[i] = BuildFrame(fs)
		totalBytes += len(rawFrames[i])
	}

	corruptFrom := len(frames)
	truncated := cfg.Scenario == ScenarioTruncated
	if truncated {
		corruptFrom = len(frames) * 7 / 10
	}
	data, err := buildSegmentBytes(spec, seq, rawFrames, corruptFrom, truncated)
	if err != nil {
		return err
	}
	path := segmentPath(fx, spec.ID, seq)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return merr.Wrap(err, "fixture", path, "segment write failed")
	}

	lost := 0
	if cfg.Scenario == ScenarioBurstLoss && spec.ID == 1 {
		lost = burstLossCount
	}
	meta := segmentMeta{
		TrackID: spec.ID, Seq: seq, FrameCount: len(frames),
		MediaBytes: totalBytes, LostPackets: lost,
		Truncated: truncated, CorruptFrom: corruptFrom,
	}
	md, err := json.Marshal(meta)
	if err != nil {
		return merr.Wrap(err, "fixture", "meta", "encode failed")
	}
	metaPath := filepath.Join(fx.TrackDir[spec.ID],
		"seg-"+uitoa(uint64(seq))+".meta.json")
	if err := os.WriteFile(metaPath, md, 0o644); err != nil {
		return merr.Wrap(err, "fixture", metaPath, "meta write failed")
	}
	return nil
}

func segmentPath(fx *Fixture, trackID, seq uint32) string {
	return filepath.Join(fx.TrackDir[trackID], "seg-"+uitoa(uint64(seq))+".m4s")
}

func capturePath(fx *Fixture, trackID, seq uint32) string {
	return filepath.Join(fx.TrackDir[trackID], "seg-"+uitoa(uint64(seq))+".rtp")
}
