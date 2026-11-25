// Package fixture generates all local MP4/RTP sample material used by demos and tests.
package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacore/internal/mp4"
)

const (
	NormalSamples    = 720
	SampleDeltaMS    = 1000
	SampleSize       = 16
	KeyframeEvery    = 30
	StartPTSOffset   = 250
	EmptyEditMS      = 2000
	MediaOffsetMS    = 250
	NormalMovieScale = 1000
	RTPScale         = 90000
)

// Set describes a generated fixture directory.
type Set struct {
	Root         string `json:"root"`
	ManifestPath string `json:"manifest_path"`
}

// GenerateNormal creates a long sparse-keyframe MP4 and a wrapping RTP source.
func GenerateNormal(root string) (*Set, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	mp4Path := filepath.Join(root, "normal.mp4")
	if err := os.WriteFile(mp4Path, buildNormalMP4(), 0o644); err != nil {
		return nil, err
	}
	rtpPath := filepath.Join(root, "timeline.rtp")
	if err := os.WriteFile(rtpPath, buildNormalRTP(), 0o644); err != nil {
		return nil, err
	}
	manifest := mp4.Manifest{
		Name:           "generated-normal",
		MovieTimeScale: NormalMovieScale,
		Sources: []mp4.Source{
			{ID: "video", Kind: mp4.SourceMP4, Path: "normal.mp4", TrackNumber: 1, TimeScale: NormalMovieScale, Role: "video"},
			{ID: "rtp-metadata", Kind: mp4.SourceRTP, Path: "timeline.rtp", TrackNumber: 2, TimeScale: RTPScale, Role: "rtp-metadata"},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return nil, err
	}
	return &Set{Root: root, ManifestPath: manifestPath}, nil
}

func buildNormalMP4() []byte {
	ftyp := ftypBox()
	sampleData := make([]byte, NormalSamples*SampleSize)
	for i := 0; i < NormalSamples; i++ {
		binary.BigEndian.PutUint32(sampleData[i*SampleSize:], uint32(i+1))
	}
	mdat := box("mdat", sampleData)
	mdatPayloadOffset := uint32(len(ftyp) + 8)

	var sync []uint32
	for i := 0; i < NormalSamples; i += KeyframeEvery {
		sync = append(sync, uint32(i+1))
	}
	stbl := box("stbl", concat(
		stsdBox(),
		sttsUniformBox(NormalSamples, SampleDeltaMS),
		cttsUniformBox(NormalSamples, StartPTSOffset),
		stssBox(sync),
		stscOneChunkBox(NormalSamples),
		stszUniformBox(NormalSamples, SampleSize),
		stcoOneBox(mdatPayloadOffset),
	))
	elst := elstV0Box([]EditSpec{
		{Duration: EmptyEditMS, MediaTime: -1},
		{Duration: NormalSamples * SampleDeltaMS, MediaTime: MediaOffsetMS},
	})
	edts := edtsBox(elst)
	mdhd := mdhdBox(NormalMovieScale, NormalSamples*SampleDeltaMS)
	mdia := box("mdia", concat(mdhd, hdlrBox(), box("minf", stbl)))
	tkhd := tkhdBox(1, EmptyEditMS+NormalSamples*SampleDeltaMS)
	trak := box("trak", concat(tkhd, edts, mdia))
	mvhd := mvhdBox(NormalMovieScale, EmptyEditMS+NormalSamples*SampleDeltaMS)
	moov := box("moov", concat(mvhd, trak))

	return concat(ftyp, mdat, moov)
}

func buildNormalRTP() []byte {
	out := []byte(mp4.RTPMagic)
	seq := uint16(65520)
	ts := uint32(0xffff0000)
	for i := 0; i < NormalSamples; i++ {
		pkt := &rtp.Packet{Header: rtp.Header{
			Version: 2, Padding: false, Marker: i%KeyframeEvery == 0,
			PayloadType: 96, SequenceNumber: seq + uint16(i),
			Timestamp: ts + uint32(i)*RTPScale, SSRC: 0x12345678,
		}, Payload: make([]byte, 12)}
		binary.BigEndian.PutUint32(pkt.Payload[0:4], uint32(i+1))
		raw, _ := pkt.Marshal()
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out = append(out, lenBuf[:]...)
		out = append(out, raw...)
	}
	return out
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
