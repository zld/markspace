package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacore/internal/mp4"
)

// CorruptCase identifies one generated negative/ boundary fixture.
type CorruptCase struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Path          string `json:"path"`
	Category      string `json:"expected_category"`
	TrackNumber   int    `json:"track_number"`
	ExpectedEvery int    `json:"expected_every,omitempty"`
	TotalSamples  int    `json:"total_samples,omitempty"`
}

// GenerateCorrupt writes all required corruption and boundary samples.
func GenerateCorrupt(root string) ([]CorruptCase, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	normal := buildNormalMP4()
	truncPath := filepath.Join(root, "truncated-box.mp4")
	if err := os.WriteFile(truncPath, normal[:len(normal)-96], 0o644); err != nil {
		return nil, err
	}
	negPath := filepath.Join(root, "negative-duration-edit.mp4")
	if err := os.WriteFile(negPath, buildMiniMP4(60, []uint32{1, 31}, rawElstV1NegativeDuration()), 0o644); err != nil {
		return nil, err
	}
	sync := []uint32{1, 21, 31, 41, 51, 61, 71, 81, 91, 101, 111} // sample 11 omitted
	missingPath := filepath.Join(root, "missing-keyframe-index.mp4")
	if err := os.WriteFile(missingPath, buildMiniMP4(120, sync, nil), 0o644); err != nil {
		return nil, err
	}
	rtpPath := filepath.Join(root, "timestamp-wrap-ambiguous.rtp")
	if err := os.WriteFile(rtpPath, buildAmbiguousRTP(), 0o644); err != nil {
		return nil, err
	}
	intervalPath := filepath.Join(root, "overlapping-intervals.txt")
	intervals := "# event-time intervals, left closed right open\n" +
		"600000000000 604000000000\n" +
		"603000000000 607000000000\n"
	if err := os.WriteFile(intervalPath, []byte(intervals), 0o644); err != nil {
		return nil, err
	}
	return []CorruptCase{
		{Name: "truncated box", Kind: "mp4", Path: truncPath, Category: "truncated_box", TrackNumber: 1},
		{Name: "negative duration edit", Kind: "mp4", Path: negPath, Category: "negative_duration_edit", TrackNumber: 1},
		{Name: "missing keyframe index item", Kind: "missing-keyframe", Path: missingPath, Category: "missing_keyframe_index", TrackNumber: 1, ExpectedEvery: 10, TotalSamples: 120},
		{Name: "ambiguous RTP timestamp wrap", Kind: "rtp", Path: rtpPath, Category: "timestamp_wrap_ambiguous", TrackNumber: 3},
		{Name: "overlapping intervals", Kind: "intervals", Path: intervalPath, Category: "overlapping_interval"},
	}, nil
}

func buildMiniMP4(sampleCount uint32, sync []uint32, rawElst []byte) []byte {
	ftyp := ftypBox()
	data := make([]byte, int(sampleCount)*SampleSize)
	for i := range sampleCount {
		binary.BigEndian.PutUint32(data[i*SampleSize:], i+1)
	}
	mdat := box("mdat", data)
	mdatPayloadOffset := uint32(len(ftyp) + 8)
	stbl := box("stbl", concat(
		stsdBox(),
		sttsUniformBox(sampleCount, SampleDeltaMS),
		stssBox(sync),
		stscOneChunkBox(sampleCount),
		stszUniformBox(sampleCount, SampleSize),
		stcoOneBox(mdatPayloadOffset),
	))
	trakParts := [][]byte{tkhdBox(1, sampleCount*SampleDeltaMS)}
	if rawElst != nil {
		trakParts = append(trakParts, edtsBox(rawElst))
	}
	mdia := box("mdia", concat(
		mdhdBox(NormalMovieScale, sampleCount*SampleDeltaMS),
		hdlrBox(),
		box("minf", stbl),
	))
	trakParts = append(trakParts, mdia)
	trak := box("trak", concat(trakParts...))
	moov := box("moov", concat(mvhdBox(NormalMovieScale, sampleCount*SampleDeltaMS), trak))
	return concat(ftyp, mdat, moov)
}

func buildAmbiguousRTP() []byte {
	out := []byte(mp4.RTPMagic)
	packets := []rtp.Header{
		{Version: 2, PayloadType: 96, SequenceNumber: 100, Timestamp: 0, SSRC: 9},
		{Version: 2, PayloadType: 96, SequenceNumber: 101, Timestamp: 1 << 31, SSRC: 9},
	}
	for i, h := range packets {
		pkt := &rtp.Packet{Header: h, Payload: []byte{byte(i), 0, 0, 0}}
		raw, _ := pkt.Marshal()
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out = append(out, lenBuf[:]...)
		out = append(out, raw...)
	}
	return out
}
