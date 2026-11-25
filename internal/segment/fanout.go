// Package segment fans planning facts out into independent representation
// layers and validates packetized fragment boundaries.
package segment

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
	"archivecheck/internal/recovery"
)

const FragmentsPerLayer = 2

// FragmentBundle is one encoded fragment and its audit facts.
type FragmentBundle struct {
	Index    int
	Bytes    []byte
	Hash     string
	Summary  model.ArtifactSummary
	Boundary model.Boundary
	Recovery model.RecoveryPosition
}

// LayerBuild contains a complete deterministic representation.
type LayerBuild struct {
	Report    model.LayerReport
	Fragments []FragmentBundle
}

// BuildLayer constructs all fragments for one bitrate. Other bitrates are not
// touched by this function.
func BuildLayer(assetID, recordDir string, bitrate int64, scan *mp4scan.ScanResult, interval model.EditInterval) (*LayerBuild, error) {
	out := &LayerBuild{Report: model.LayerReport{Bitrate: bitrate, Verified: false}}
	for i := 0; i < FragmentsPerLayer; i++ {
		request := recovery.NextRequestTick(interval, i, FragmentsPerLayer)
		pos, err := recovery.Position(scan, bitrate, i, request)
		if err != nil {
			return nil, err
		}
		boundary := model.Boundary{
			FragmentIndex: i,
			StartTick:     request,
			EndTick:       fragmentEnd(interval, i),
		}
		b, err := EncodeFragment(assetID, bitrate, i, pos, boundary)
		if err != nil {
			return nil, err
		}
		if err := VerifyPacketBoundaries(b); err != nil {
			return nil, err.WithBitrate(bitrate)
		}
		hash := model.SHA256Bytes(b)
		rel := fmt.Sprintf("artifacts/%s/%s/%d/fragment-%d.rtpfrag", recordDir, assetID, bitrate, i)
		fb := FragmentBundle{
			Index: i, Bytes: b, Hash: hash, Boundary: boundary, Recovery: pos,
			Summary: model.ArtifactSummary{
				Path: rel, Kind: "rtp-fragment", SizeBytes: int64(len(b)), SHA256: hash,
			},
		}
		out.Fragments = append(out.Fragments, fb)
		out.Report.Recovery = append(out.Report.Recovery, pos)
		out.Report.Boundaries = append(out.Report.Boundaries, boundary)
	}
	out.Report.ProcessedFragments = len(out.Fragments)
	out.Report.Cursor = len(out.Fragments)
	out.Report.Verified = true
	return out, nil
}

func fragmentEnd(in model.EditInterval, index int) int64 {
	if index == FragmentsPerLayer-1 {
		return in.EndTick
	}
	step := (in.EndTick - in.StartTick) / FragmentsPerLayer
	return in.StartTick + step*int64(index+1)
}

// EncodeFragment creates a deterministic length-prefixed sequence of RTP
// packets. The container format is local to archivecheck fixtures.
func EncodeFragment(assetID string, bitrate int64, index int, pos model.RecoveryPosition, boundary model.Boundary) ([]byte, error) {
	var out []byte
	packetCount := 3
	ssrc := uint32(bitrate)
	baseSeq := uint16((bitrate/100 + int64(index)*17) & 0xffff)
	for j := 0; j < packetCount; j++ {
		seq := baseSeq + uint16(j)
		ts := uint32(boundary.StartTick + int64(j)*int64(scanSampleDelta()))
		payload := []byte(fmt.Sprintf("AC|%s|%d|%d|%d|%d", assetID, bitrate, index, pos.ActualTick, j))
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
				Marker:         j == packetCount-1,
			},
			Payload: payload,
		}
		raw, err := p.Marshal()
		if err != nil {
			return nil, apperrors.Wrap(apperrors.KindLayer, err)
		}
		frame := make([]byte, 4+len(raw))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
		copy(frame[4:], raw)
		out = append(out, frame...)
	}
	return out, nil
}

func scanSampleDelta() int { return 3_000 }

// VerifyPacketBoundaries decodes every length-prefixed RTP packet with
// pion/rtp and checks sequence continuity and terminal marker.
func VerifyPacketBoundaries(b []byte) *apperrors.Error {
	off := 0
	packets := 0
	var lastSeq uint16
	var ssrc uint32
	for off < len(b) {
		if len(b)-off < 4 {
			return apperrors.E(apperrors.KindBoundary, "short RTP length prefix")
		}
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		off += 4
		if n <= 0 || off+n > len(b) {
			return apperrors.E(apperrors.KindBoundary, "RTP packet length exceeds fragment")
		}
		var p rtp.Packet
		if err := p.Unmarshal(b[off : off+n]); err != nil {
			return apperrors.Wrap(apperrors.KindBoundary, err)
		}
		if p.Version != 2 {
			return apperrors.E(apperrors.KindBoundary, "invalid RTP version")
		}
		if packets == 0 {
			ssrc = p.SSRC
		} else {
			if p.SSRC != ssrc {
				return apperrors.E(apperrors.KindBoundary, "SSRC changed inside fragment")
			}
			if p.SequenceNumber != lastSeq+1 {
				return apperrors.E(apperrors.KindBoundary, "RTP sequence discontinuity")
			}
		}
		lastSeq = p.SequenceNumber
		off += n
		packets++
	}
	if packets == 0 {
		return apperrors.E(apperrors.KindBoundary, "fragment contains no RTP packets")
	}
	var final rtp.Packet
	// The final packet marker is checked by decoding the last frame directly
	// through the same pion unmarshaller path.
	if err := final.Unmarshal(lastPacketBytes(b)); err != nil {
		return apperrors.Wrap(apperrors.KindBoundary, err)
	}
	if !final.Marker {
		return apperrors.E(apperrors.KindBoundary, "fragment final RTP packet lacks marker")
	}
	return nil
}

func lastPacketBytes(b []byte) []byte {
	off := 0
	var raw []byte
	for off < len(b) {
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		off += 4
		raw = b[off : off+n]
		off += n
	}
	return raw
}
