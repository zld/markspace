// Package pipeline wires manifest parsing, RTP recovery, fMP4 sample
// extraction, timeline composition, quality statistics and frame export
// into the task DAG. Ingestion is streaming and bounded: one segment is
// recovered at a time through the bounded reorder window.
package pipeline

import (
	"bytes"
	"encoding/binary"
	"os"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"archivecheck/internal/fixture"
	"archivecheck/internal/merr"
)

// readCapture loads one RTP capture file and returns its datagrams in
// file order (which may be out of order or contain duplicates).
func readCapture(path string) ([]*rtp.Packet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, merr.Wrap(err, "pipeline", path, "cannot read RTP capture")
	}
	if len(data) < fixture.CaptureHeaderLen ||
		!bytes.Equal(data[0:4], fixture.CaptureMagic[:]) {
		return nil, merr.New(merr.KindContainer, "pipeline", path,
			"not an ARCH RTP capture file")
	}
	if data[4] != fixture.CaptureVersion {
		return nil, merr.New(merr.KindContainer, "pipeline", path,
			"unsupported RTP capture version")
	}
	var pkts []*rtp.Packet
	pos := fixture.CaptureHeaderLen
	for pos < len(data) {
		if pos+4 > len(data) {
			return nil, merr.New(merr.KindDecode, "pipeline", path,
				"truncated datagram length prefix")
		}
		n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if n == 0 || pos+n > len(data) {
			return nil, merr.New(merr.KindDecode, "pipeline", path,
				"datagram length exceeds capture file")
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(data[pos : pos+n]); err != nil {
			return nil, merr.Wrap(err, "pipeline", path, "malformed RTP datagram")
		}
		pkts = append(pkts, pkt)
		pos += n
	}
	return pkts, nil
}

// sampleInfo describes one sample declared by a segment's trun box.
type sampleInfo struct {
	Size uint32
}

// parseSegmentSamples reads the fMP4 trun sample table and the mdat
// payload from one generated segment. Only the fixture subset
// (styp/moof/traf/tfhd/tfdt/trun/mdat) is accepted.
func parseSegmentSamples(path string) (samples []sampleInfo, mdat []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, merr.Wrap(err, "pipeline", path, "cannot open segment")
	}
	defer f.Close()

	supported := map[string]bool{
		"styp": true, "moof": true, "traf": true, "mfhd": true,
		"tfhd": true, "tfdt": true, "trun": true, "mdat": true,
	}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		name := h.BoxInfo.Type.String()
		if !supported[name] {
			return nil, merr.New(merr.KindContainer, "pipeline", path,
				"unsupported fMP4 box in fixture subset: "+name)
		}
		switch name {
		case "moof", "traf":
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case "trun":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			trun := box.(*mp4.Trun)
			for _, e := range trun.Entries {
				samples = append(samples, sampleInfo{Size: e.SampleSize})
			}
		case "mdat":
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			mdat = buf.Bytes()
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		if me, ok := merr.As(err); ok {
			return nil, nil, me
		}
		return nil, nil, merr.Wrap(err, "pipeline", path, "fMP4 parse failed")
	}
	if len(samples) == 0 {
		return nil, nil, merr.New(merr.KindContainer, "pipeline", path,
			"segment trun declares no samples")
	}
	return samples, mdat, nil
}

// extractFrameBytes slices mdat into one byte range per declared sample.
// A truncated mdat yields short final samples, which the caller marks
// corrupt instead of padding.
func extractFrameBytes(samples []sampleInfo, mdat []byte) ([][]byte, bool) {
	out := make([][]byte, 0, len(samples))
	truncated := false
	pos := 0
	for _, s := range samples {
		end := pos + int(s.Size)
		if end > len(mdat) {
			end = len(mdat)
			truncated = true
		}
		if pos >= len(mdat) {
			out = append(out, nil)
			truncated = true
			continue
		}
		out = append(out, mdat[pos:end])
		pos += int(s.Size)
	}
	return out, truncated
}
