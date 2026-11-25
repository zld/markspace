package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"

	"github.com/pion/rtp/v2"
)

// RTPFileName is the per-representation transmission record written by the
// generator and consumed by the scan subsystem.
const RTPFileName = "stream.rtp"

// rtpClockRate is the reference clock for every generated RTP stream. RTP
// timestamps therefore share one rate even when MP4 timescales differ.
const rtpClockRate = 90000

// rtpMarkerPayloadSize is the deterministic fragment marker carried inside
// each RTP payload: input index followed by mfhd sequence number.
const rtpMarkerPayloadSize = 8

// writeRTPStreams serializes one length-prefixed RTP packet per fragment
// arrival. Framing is a two-byte big-endian length before each
// rtp.Packet.Marshal() blob so any reader can recover packet boundaries.
func writeRTPStreams(cfg Config, descs []RepresentationDescriptor) error {
	for _, rep := range descs {
		entries, err := fragmentArrivals(cfg, rep)
		if err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(rep.Directory, RTPFileName))
		if err != nil {
			return err
		}
		for i, e := range entries {
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(i),
					Timestamp:      uint32(e.baseNanos * rtpClockRate / 1_000_000_000),
					SSRC:           0x4d454449 + uint32(rep.TrackID),
					Marker:         i == len(entries)-1,
				},
				Payload: rtpMarkerPayload(e.inputIndex, e.mfhd),
			}
			blob, err := pkt.Marshal()
			if err != nil {
				f.Close()
				return err
			}
			frame := make([]byte, 2+len(blob))
			binary.BigEndian.PutUint16(frame, uint16(len(blob)))
			copy(frame[2:], blob)
			if _, err := f.Write(frame); err != nil {
				f.Close()
				return err
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

type arrival struct {
	inputIndex uint32
	mfhd       uint32
	baseNanos  int64
}

// fragmentArrivals returns arrivals in transmission order, including any
// duplicate mfhd arrivals, by inspecting what was written to disk.
func fragmentArrivals(cfg Config, rep RepresentationDescriptor) ([]arrival, error) {
	names, err := listFragments(rep.Directory)
	if err != nil {
		return nil, err
	}
	out := make([]arrival, 0, len(names))
	for inputIdx, name := range names {
		seq, err := parseFragmentName(name)
		if err != nil {
			return nil, err
		}
		base := rep.TickDuration * uint64(seq-1)
		if cfg.Case == CaseTFDTRegression && seq == regressionSeq {
			base = regressionTicks
		}
		if cfg.Case == CaseMisaligned && rep.TrackID == 2 {
			base += rep.TickDuration * 5
		}
		if cfg.Case == CaseDuplicateConflict && seq >= 100 {
			base = rep.TickDuration * uint64(conflictSeq-1) + rep.TickDuration/2
		}
		out = append(out, arrival{
			inputIndex: uint32(inputIdx),
			mfhd:       canonicalSeq(seq),
			baseNanos:  ticksToNanos(rep.Timescale, base),
		})
	}
	return out, nil
}

// canonicalSeq maps the duplicate arrival's file index back to the mfhd
// number it carries (frag-104.m4s repeats mfhd 4).
func canonicalSeq(fileSeq uint32) uint32 {
	if fileSeq >= 100 {
		return fileSeq - 100
	}
	return fileSeq
}

func rtpMarkerPayload(inputIndex, mfhd uint32) []byte {
	p := make([]byte, rtpMarkerPayloadSize)
	binary.BigEndian.PutUint32(p[0:4], inputIndex)
	binary.BigEndian.PutUint32(p[4:8], mfhd)
	return p
}

// listFragments returns fragment file names sorted by their sequence suffix,
// which is the deterministic input index order.
func listFragments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if len(e.Name()) >= 5 && e.Name()[:5] == "frag-" {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := parseFragmentName(names[i])
		b, _ := parseFragmentName(names[j])
		return a < b
	})
	return names, nil
}

func ticksToNanos(timescale uint32, ticks uint64) int64 {
	return int64(ticks * 1_000_000_000 / uint64(timescale))
}
