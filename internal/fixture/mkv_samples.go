package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"continuity/internal/media"
)

// MKVSpec controls deterministic generated Matroska-like samples.
type MKVSpec struct {
	Seed           int64
	Clusters       int
	UnknownSegment bool
	AudioTrack     uint32
	VideoTrack     uint32
}

// BuildMKV builds a valid interleaved Matroska-like byte stream.
func BuildMKV(spec MKVSpec) []byte {
	if spec.Clusters <= 0 {
		spec.Clusters = 3
	}
	if spec.AudioTrack == 0 {
		spec.AudioTrack = 2
	}
	if spec.VideoTrack == 0 {
		spec.VideoTrack = 1
	}
	rng := rand.New(rand.NewSource(spec.Seed))

	var segmentPayload []byte
	segmentPayload = append(segmentPayload, buildInfo()...)
	segmentPayload = append(segmentPayload, buildTracks()...)
	for c := 0; c < spec.Clusters; c++ {
		segmentPayload = append(segmentPayload, buildCluster(c, spec, rng)...)
	}

	var out []byte
	out = append(out, buildEBMLHeader()...)
	if spec.UnknownSegment {
		out = append(out, UnknownElement(media.IDSegment, segmentPayload, 8)...)
	} else {
		out = append(out, Element(media.IDSegment, segmentPayload)...)
	}
	return out
}

func buildEBMLHeader() []byte {
	payload := Element(media.IDDocType, []byte("matroska"))
	payload = append(payload, UintElement(0x4286, 1)...) // EBMLVersion
	payload = append(payload, UintElement(0x42F7, 1)...) // EBMLReadVersion
	return Element(media.IDEBMLEBML, payload)
}

func buildInfo() []byte {
	payload := UintElement(media.IDTimecodeScale, 1_000_000)
	payload = append(payload, StringElement(media.IDMuxingApp, "continuity-fixture")...)
	payload = append(payload, StringElement(media.IDWritingApp, "continuity-demo")...)
	return Element(media.IDInfo, payload)
}

func buildTracks() []byte {
	videoEntry := UintElement(media.IDTrackNumber, 1)
	videoEntry = append(videoEntry, UintElement(media.IDTrackUID, 1)...)
	videoEntry = append(videoEntry, UintElement(media.IDTrackType, 1)...)
	videoEntry = append(videoEntry, StringElement(media.IDCodecID, "V_VP9")...)
	video := UintElement(0xB0, 640) // PixelWidth
	video = append(video, UintElement(0xBA, 360)...)
	videoEntry = append(videoEntry, Element(media.IDVideo, video)...)

	audioEntry := UintElement(media.IDTrackNumber, 2)
	audioEntry = append(audioEntry, UintElement(media.IDTrackUID, 2)...)
	audioEntry = append(audioEntry, UintElement(media.IDTrackType, 2)...)
	audioEntry = append(audioEntry, StringElement(media.IDCodecID, "A_OPUS")...)
	audio := FloatElement(media.IDSamplingFrequency, 44100)
	audio = append(audio, UintElement(media.IDChannels, 6)...)
	audio = append(audio, StringElement(media.IDChannelLayout, "5.1")...)
	audioEntry = append(audioEntry, Element(media.IDAudio, audio)...)

	payload := Element(media.IDTrackEntry, videoEntry)
	payload = append(payload, Element(media.IDTrackEntry, audioEntry)...)
	return Element(media.IDTracks, payload)
}

func buildCluster(cluster int, spec MKVSpec, rng *rand.Rand) []byte {
	startMS := int64(cluster * 400)
	payload := UintElement(media.IDTimecode, uint64(startMS))
	for tick := 0; tick < 20; tick++ {
		relMS := int16(tick * 20)
		globalTick := cluster*20 + tick
		audioPayload := deterministicPayload(rng, spec.Seed, spec.AudioTrack, globalTick, 24)
		payload = append(payload, SimpleBlockElement(spec.AudioTrack, relMS, false, audioPayload)...)

		if tick%2 == 0 {
			videoPayload := deterministicPayload(rng, spec.Seed, spec.VideoTrack, globalTick/2, 120)
			keyframe := tick == 0
			payload = append(payload, SimpleBlockElement(spec.VideoTrack, relMS, keyframe, videoPayload)...)
		}
	}
	return Element(media.IDCluster, payload)
}

func deterministicPayload(rng *rand.Rand, seed int64, track uint32, index, size int) []byte {
	b := make([]byte, size)
	_, _ = rng.Read(b)
	// A stable four-byte prefix makes generated cluster bytes identifiable
	// even if the RNG implementation changes.
	binary.BigEndian.PutUint32(b, uint32(seed)^track^uint32(index))
	return b
}

// DescribeMKV returns a short human-readable sample description.
func DescribeMKV(data []byte) string {
	return fmt.Sprintf("matroska-like sample, %d bytes", len(data))
}
