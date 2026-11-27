// Package fixture generates all input material locally: a deterministic
// image-frame sequence wrapped in an MP4 container, an RTP packet per frame,
// and a per-frame manifest. No external media or services are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"continuity/internal/clock"
	"continuity/internal/model"
)

// Layout of the generated workspace (all names are workspace-relative).
const (
	InputDir       = "input"
	ContainerName  = "track01.mp4"
	ManifestName   = "manifest.json"
	MetadataName   = "metadata.json"
	BaseSSRC       = uint32(0xC0_FF_EE_00)
	BaseContainerV = uint32(1)
	BaseMetadataV  = uint32(1)
	FrameNanos     = int64(20_000_000) // 50 fps capture cadence
)

// Metadata is the sidecar that carries the independently versioned metadata
// revision; bumping it invalidates indexes without touching content.
type Metadata struct {
	MetadataVersion uint32 `json:"metadata_version"`
	Generator       string `json:"generator"`
	TrackCount      int    `json:"track_count"`
}

// Spec configures a generated dataset.
type Spec struct {
	Frames        int
	SourceRate    uint32
	SourceLayout  model.ChannelLayout
	TargetLUFS    float64
	ContainerRev  uint32
	MetadataRev   uint32
}

// DefaultSpec returns the standard deterministic generation parameters.
func DefaultSpec(frames int) Spec {
	return Spec{
		Frames:       frames,
		SourceRate:   48000,
		SourceLayout: model.LayoutMono,
		TargetLUFS:   -23.0,
		ContainerRev: BaseContainerV,
		MetadataRev:  BaseMetadataV,
	}
}

// pcmSample builds one deterministic little-endian int16 PCM sample.
func pcmSample(frame, sample, channel int) int16 {
	phase := float64(frame*960+sample*2+channel) * (2 * math.Pi / 64.0)
	amp := 0.10 * float64(math.MaxInt16)
	return int16(amp * math.Sin(phase))
}

// framePCM renders the raw PCM payload for one frame: 20 ms at sourceRate.
func framePCM(spec Spec, frame int) []byte {
	perFrame := int(spec.SourceRate) / 50 // 20 ms of audio
	ch := spec.SourceLayout.Channels()
	buf := make([]byte, perFrame*ch*2)
	for s := 0; s < perFrame; s++ {
		for c := 0; c < ch; c++ {
			binary.LittleEndian.PutUint16(buf[(s*ch+c)*2:], uint16(pcmSample(frame, s, c)))
		}
	}
	return buf
}

// rtpPacket wraps a payload in an RTP packet on the shared 90 kHz clock.
func rtpPacket(track, seq uint32, ts uint32, payload []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: uint16(seq),
			Timestamp:      ts,
			SSRC:           BaseSSRC + track,
			Marker:         true,
		},
		Payload: payload,
	}
	if _, err := p.Marshal(); err != nil {
		return nil, err
	}
	return p, nil
}

// writeContainer serializes an ftyp (carrying the container revision) plus an
// mdat box holding one RTP packet per frame, using go-mp4.
func writeContainer(path string, spec Spec, packets [][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	if _, err = w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: spec.ContainerRev,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}
	if _, err = mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err = w.EndBox(); err != nil {
		return err
	}

	if _, err = w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for _, pkt := range packets {
		if _, err = w.Write(pkt); err != nil {
			return err
		}
	}
	if _, err = w.EndBox(); err != nil {
		return err
	}
	return nil
}

// Generate renders the full input dataset under root/input using only
// deterministic, in-process material.
func Generate(root string, spec Spec) ([]model.Frame, error) {
	dir, err := model.SafeJoin(root, InputDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	frames := make([]model.Frame, spec.Frames)
	packets := make([][]byte, spec.Frames)
	for i := 0; i < spec.Frames; i++ {
		pcm := framePCM(spec, i)
		capture := int64(i) * FrameNanos
		ts, err := clock.RTPTimestamp(capture, clock.MediaClock)
		if err != nil {
			return nil, err
		}
		pkt, err := rtpPacket(1, uint32(i+1), ts, pcm)
		if err != nil {
			return nil, err
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		packets[i] = raw

		frames[i] = model.Frame{
			Track:         1,
			Sequence:      uint32(i + 1),
			CaptureNanos:  capture,
			ContainerID:   ContainerName,
			SampleRate:    spec.SourceRate,
			Channels:      spec.SourceLayout,
			TargetLUFS:    spec.TargetLUFS,
			PayloadLength: uint32(len(pcm)),
			SHA256:        model.Checksum(pcm),
			Payload:       pcm,
			Number:        model.FrameNumber(i + 1),
		}
	}

	containerPath, err := model.SafeJoin(root, filepath.Join(InputDir, ContainerName))
	if err != nil {
		return nil, err
	}
	if err = writeContainer(containerPath, spec, packets); err != nil {
		return nil, err
	}

	meta := Metadata{
		MetadataVersion: spec.MetadataRev,
		Generator:       "continuity/fixture",
		TrackCount:      1,
	}
	if err = writeJSON(root, filepath.Join(InputDir, MetadataName), meta); err != nil {
		return nil, err
	}
	if err = writeJSON(root, filepath.Join(InputDir, ManifestName), manifestDoc{Frames: frames}); err != nil {
		return nil, err
	}
	return frames, nil
}

type manifestDoc struct {
	Frames []model.Frame `json:"frames"`
}

func writeJSON(root, rel string, v any) error {
	path, err := model.SafeJoin(root, rel)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return model.AtomicWrite(path, data, 0o644)
}

// ParseRTP round-trips an RTP packet to prove the bytes are well-formed.
func ParseRTP(raw []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return nil, err
	}
	return p, nil
}

// ContainerHeader reads ftyp + mdat box metadata back from a container.
func ContainerHeader(data []byte) (minorVersion uint32, mdat *mp4.BoxInfo, err error) {
	r := bytes.NewReader(data)
	boxes, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMdat()},
	})
	if err != nil {
		return 0, nil, err
	}
	var ftyp mp4.Ftyp
	var mdatInfo *mp4.BoxInfo
	for _, bi := range boxes {
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			if _, err = bi.SeekToPayload(r); err != nil {
				return 0, nil, err
			}
			if _, err = mp4.Unmarshal(r, bi.Size-bi.HeaderSize, &ftyp, mp4.Context{}); err != nil {
				return 0, nil, err
			}
		case mp4.BoxTypeMdat():
			mdatInfo = bi
		}
	}
	return ftyp.MinorVersion, mdatInfo, nil
}
