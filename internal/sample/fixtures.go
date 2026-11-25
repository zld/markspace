package sample

import (
	"fmt"

	"mediacheck/internal/ebml"
)

type scheduleBlock struct {
	track   uint64
	ms      int16
	flags   byte
	rtpTS   uint32
	pt      uint8
	seqBase uint16
}

func buildNormal(seed int64, truncatedCluster, rtpGap bool) ([]byte, [][]byte, error) {
	head := buildHeader()
	info := buildInfo()
	tracks := buildNormalTracks()
	clusterPayload, rtpRaw := buildNormalCluster(seed, rtpGap)

	var clusterHeader []byte
	if truncatedCluster {
		// Declare 64 more bytes than are physically appended. The stable input
		// end is therefore the truncation offset reported by the EBML reader.
		clusterHeader = append(idBytes(ebml.IDCluster), sizeVINT(len(clusterPayload)+64)...)
	} else {
		clusterHeader = unknownElement(ebml.IDCluster, 4)
	}
	cluster := append(clusterHeader, clusterPayload...)
	segmentPayload := append(append(info, tracks...), cluster...)
	segment := append(unknownElement(ebml.IDSegment, 8), segmentPayload...)
	return append(head, segment...), rtpRaw, nil
}

func buildAudioConflict(seed int64) ([]byte, [][]byte, error) {
	head := buildHeader()
	info := buildInfo()
	tracks := buildConflictTracks()

	var payload []byte
	payload = append(payload, uintElement(ebml.IDTimecode, 0)...)
	blocks := []struct {
		track uint64
		ms    int16
	}{{1, 0}, {2, 0}, {1, 20}, {2, 20}}
	for i, b := range blocks {
		frame := frameBytes(seed, int(b.track), i)
		payload = append(payload, simpleBlock(b.track, b.ms, 0x80, frame)...)
	}
	cluster := append(unknownElement(ebml.IDCluster, 4), payload...)
	segmentPayload := append(append(info, tracks...), cluster...)
	segment := append(unknownElement(ebml.IDSegment, 8), segmentPayload...)
	return append(head, segment...), nil, nil
}

func buildNormalCluster(seed int64, rtpGap bool) ([]byte, [][]byte) {
	schedule := []scheduleBlock{
		{track: 1, ms: 0, flags: 0x80, rtpTS: 0, pt: 96, seqBase: 1000},
		{track: 2, ms: 0, flags: 0x80, rtpTS: 0, pt: 97, seqBase: 2000},
		{track: 2, ms: 20, flags: 0x80, rtpTS: 960, pt: 97, seqBase: 2000},
		{track: 1, ms: 33, flags: 0x00, rtpTS: 2970, pt: 96, seqBase: 1000},
		{track: 2, ms: 40, flags: 0x00, rtpTS: 1920, pt: 97, seqBase: 2000},
		{track: 1, ms: 66, flags: 0x00, rtpTS: 5940, pt: 96, seqBase: 1000},
	}
	clusterPayload := uintElement(ebml.IDTimecode, 0)
	var rtp [][]byte
	trackIndex := map[uint64]int{}
	for order, item := range schedule {
		idx := trackIndex[item.track]
		trackIndex[item.track] = idx + 1
		frame := frameBytes(seed, int(item.track), idx)
		clusterPayload = append(clusterPayload, simpleBlock(item.track, item.ms, item.flags, frame)...)

		// rtp-gap removes the middle audio packet (track-2 index 1), while
		// retaining its EBML block so the mapped input offset remains available.
		if rtpGap && item.track == 2 && idx == 1 {
			continue
		}
		var ssrc uint32 = SSRCVideo
		if item.track == 2 {
			ssrc = SSRCAudio
		}
		raw, err := marshalRTP(item.seqBase+uint16(idx), item.rtpTS, ssrc, item.pt, idx == 2, frame)
		if err != nil {
			panic(fmt.Sprintf("marshal generated RTP: %v", err))
		}
		rtp = append(rtp, raw)
		_ = order
	}
	return clusterPayload, rtp
}

func frameBytes(seed int64, track, index int) []byte {
	const frameSize = 24
	p := make([]byte, frameSize)
	label := []byte(fmt.Sprintf("s%02d-t%d-b%d", seed, track, index))
	copy(p, label)
	for i := len(label); i < frameSize; i++ {
		p[i] = byte(seed*3 + int64(track)*31 + int64(index)*17 + int64(i))
	}
	return p
}
