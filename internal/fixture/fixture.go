// Package fixture generates deterministic, self-contained Matroska/WebM
// test inputs. No external media is referenced: audio is synthesized PCM and
// every byte is derived from fixed parameters, so repeated generation yields
// byte-identical files (and therefore identical SHA-256 summaries).
package fixture

import (
	"encoding/binary"
	"math"
	"os"
)

// Element IDs (canonical VINT forms) and constants reused by the writer.
const (
	idEBML          = 0x1A45DFA3
	idDocType       = 0x4282
	idSegment       = 0x18538067
	idInfo          = 0x1549A966
	idTimecodeScale = 0x2AD7B1
	idMuxingApp     = 0x4D80
	idWritingApp    = 0x5741
	idTracks        = 0x1654AE6B
	idTrackEntry    = 0xAE
	idTrackNumber   = 0xD7
	idTrackType     = 0x83
	idCodecID       = 0x86
	idAudio         = 0xE1
	idSampleFreq    = 0xB5
	idChannels      = 0x9F
	idCluster       = 0x1F43B675
	idTimecode      = 0xE7
	idSimpleBlock   = 0xA3
)

// SampleRate matches the delivery clock so block times align to whole frames.
const SampleRate = 48000

// BlockSamples is 0.5 s of stereo 16-bit PCM per block.
const BlockSamples = 24000

func beU(v uint64, n int) []byte {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

func beF64(v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return b
}

// vintSize writes a data VINT (marker ORed into the narrowest width).
func vintSize(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<uint(7*width))-1 {
			b := make([]byte, width)
			x := v
			for i := width - 1; i >= 0; i-- {
				b[i] = byte(x)
				x >>= 8
			}
			b[0] |= 0x80 >> uint(width-1)
			return b
		}
	}
	panic("size vint too large")
}

// vintID emits a canonical ID constant verbatim at its marker's width.
func vintID(v uint64) []byte {
	for width := 4; width >= 1; width-- {
		markerBit := uint(7 * width)
		if v&(uint64(1)<<markerBit) != 0 {
			b := make([]byte, width)
			x := v
			for i := width - 1; i >= 0; i-- {
				b[i] = byte(x)
				x >>= 8
			}
			return b
		}
	}
	panic("id without marker")
}

func elem(id uint64, body []byte) []byte {
	return append(vintID(id), append(vintSize(uint64(len(body))), body...)...)
}

// pcmBlock synthesizes a deterministic stereo sine for track at timeIndex.
func pcmBlock(track, timeIndex int) []byte {
	buf := make([]byte, BlockSamples*2*2) // stereo, int16
	freq := 440.0 + float64(track)*110.0
	for i := 0; i < BlockSamples; i++ {
		phase := 2 * math.Pi * freq * float64(i) / SampleRate
		// Distinct phase per block keeps every payload unique.
		sample := int16(0.4 * math.MaxInt16 * math.Sin(phase+float64(timeIndex)))
		binary.LittleEndian.PutUint16(buf[i*4:], uint16(sample))
		binary.LittleEndian.PutUint16(buf[i*4+2:], uint16(sample))
	}
	return buf
}

// blockVint encodes a Matroska block track number (1-based, matching
// TrackEntry.TrackNumber) as a data VINT: track 1 -> 0x81, track 2 -> 0x82.
func blockVint(trackNo uint64) []byte {
	return vintSize(trackNo)
}

// simpleBlock writes one SimpleBlock for the given 1-based track number.
func simpleBlock(trackNo uint64, rel int16, frame []byte) []byte {
	head := append(blockVint(trackNo), beU(uint64(uint16(rel)), 2)...)
	head = append(head, 0x80) // keyframe flag
	return elem(idSimpleBlock, append(head, frame...))
}

func trackEntry(trackNo uint64) []byte {
	var body []byte
	body = append(body, elem(idTrackNumber, beU(trackNo, 1))...)
	body = append(body, elem(idTrackType, beU(2, 1))...)
	body = append(body, elem(idCodecID, []byte("A_PCM/INT/LIT"))...)
	var audio []byte
	audio = append(audio, elem(idSampleFreq, beF64(SampleRate))...)
	audio = append(audio, elem(idChannels, beU(2, 1))...)
	body = append(body, elem(idAudio, audio)...)
	return elem(idTrackEntry, body)
}

// BuildWebM assembles the canonical two-track, four-block fixture. Two
// clusters each interleave one block per track, at t=0 and t=0.5 s.
func BuildWebM() []byte {
	ebmlHdr := elem(idEBML, elem(idDocType, []byte("webm")))
	var info []byte
	info = append(info, elem(idTimecodeScale, beU(1_000_000, 4))...)
	info = append(info, elem(idMuxingApp, []byte("mediatrace-fixture"))...)
	info = append(info, elem(idWritingApp, []byte("mediatrace-fixture"))...)
	infoElem := elem(idInfo, info)

	var tracks []byte
	tracks = append(tracks, trackEntry(1)...)
	tracks = append(tracks, trackEntry(2)...)
	tracksElem := elem(idTracks, tracks)

	var c1 []byte
	c1 = append(c1, elem(idTimecode, beU(0, 4))...)
	c1 = append(c1, simpleBlock(1, 0, pcmBlock(1, 0))...)
	c1 = append(c1, simpleBlock(2, 0, pcmBlock(2, 0))...)
	c1Elem := elem(idCluster, c1)

	var c2 []byte
	c2 = append(c2, elem(idTimecode, beU(500, 4))...)
	c2 = append(c2, simpleBlock(1, 0, pcmBlock(1, 1))...)
	c2 = append(c2, simpleBlock(2, 0, pcmBlock(2, 1))...)
	c2Elem := elem(idCluster, c2)

	var segmentBody []byte
	segmentBody = append(segmentBody, infoElem...)
	segmentBody = append(segmentBody, tracksElem...)
	segmentBody = append(segmentBody, c1Elem...)
	segmentBody = append(segmentBody, c2Elem...)

	segment := elem(idSegment, segmentBody)
	return append(ebmlHdr, segment...)
}

// BuildBadWebM returns a truncated copy of the good fixture with its last
// size VINT deliberately cut, producing a stable truncation error at a fixed
// byte offset. The header prefix stays identical so the repaired variant can
// reuse the same cache slot and demonstrate cache=rebuilt.
func BuildBadWebM(good []byte) []byte {
	// Remove the final block's frame tail: the scanner reaches the last
	// element header and finds its declared payload runs past EOF.
	cut := len(good) - 64
	bad := make([]byte, cut)
	copy(bad, good[:cut])
	return bad
}

// WriteFile writes data to path atomically (temp + rename).
func WriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// BuildQuietWebM assembles a one-track fixture whose PCM amplitude is far
// below the -16 LUFS target. EBU R128 integration over such a quiet, short
// program measures near -70 LUFS, so every AAC candidate fails the loudness
// gate and the engine must report a quality/budget failure.
func BuildQuietWebM() []byte {
	ebmlHdr := elem(idEBML, elem(idDocType, []byte("webm")))
	var info []byte
	info = append(info, elem(idTimecodeScale, beU(1_000_000, 4))...)
	infoElem := elem(idInfo, info)

	var trackBody []byte
	trackBody = append(trackBody, elem(idTrackNumber, beU(1, 1))...)
	trackBody = append(trackBody, elem(idTrackType, beU(2, 1))...)
	trackBody = append(trackBody, elem(idCodecID, []byte("A_PCM/INT/LIT"))...)
	var audio []byte
	audio = append(audio, elem(idSampleFreq, beF64(SampleRate))...)
	audio = append(audio, elem(idChannels, beU(1, 1))...)
	trackBody = append(trackBody, elem(idAudio, audio)...)
	tracksElem := elem(idTracks, elem(idTrackEntry, trackBody))

	// One very quiet 100 ms mono block.
	frame := make([]byte, 4800*2)
	for i := 0; i < 4800; i++ {
		sample := int16(200) // ~ -42 dBFS, well below the -16 LUFS target
		binary.LittleEndian.PutUint16(frame[i*2:], uint16(sample))
	}
	head := append(blockVint(1), beU(0, 2)...)
	head = append(head, 0x80)
	var clusterBody []byte
	clusterBody = append(clusterBody, elem(idTimecode, beU(0, 4))...)
	clusterBody = append(clusterBody, elem(idSimpleBlock, append(head, frame...))...)
	cluster := elem(idCluster, clusterBody)

	segment := elem(idSegment, append(append(infoElem, tracksElem...), cluster...))
	return append(ebmlHdr, segment...)
}
