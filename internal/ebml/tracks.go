package ebml

// Track 为轨道映射信息。
type Track struct {
	Number   uint64  `json:"number"`
	Type     uint64  `json:"type"` // 1=video 2=audio
	CodecID  string  `json:"codec_id"`
	Rate     float64 `json:"rate"`    // 音频采样率
	Channels uint64  `json:"channels"` // 音频声道数
}

// 轨道相关元素 ID。
const (
	IDTrackEntry     uint32 = 0xAE
	IDTrackNumber    uint32 = 0xD7
	IDTrackType      uint32 = 0x83
	IDCodecID        uint32 = 0x86
	IDTrackAudio     uint32 = 0xE1
	IDTrackVideo     uint32 = 0xE0
	IDSamplingFreq   uint32 = 0xB5
	IDChannels       uint32 = 0x9F
)

// parseTracks 解析 Tracks 元素（载荷较小，整体读入）。
func (p *Parser) parseTracks(e *Element, sink Sink) {
	data, err := p.readBytes(e.Size)
	if err != nil {
		sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset, Msg: "tracks truncated"})
		return
	}
	var tracks []Track
	c := &countingReader{r: bytesReader(data)}
	for c.pos < int64(len(data)) {
		sub, err := readHeaderFrom(c)
		if err != nil {
			break
		}
		if sub.ID != IDTrackEntry {
			skipN(c, sub.Size)
			continue
		}
		body, err := readBytesFrom(c, sub.Size)
		if err != nil {
			sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset, Msg: "track entry truncated"})
			break
		}
		tracks = append(tracks, parseTrackEntry(body))
	}
	sink.OnTracks(tracks)
}

func parseTrackEntry(body []byte) Track {
	t := Track{Rate: 8000, Channels: 1}
	c := &countingReader{r: bytesReader(body)}
	for c.pos < int64(len(body)) {
		sub, err := readHeaderFrom(c)
		if err != nil {
			break
		}
		switch sub.ID {
		case IDTrackNumber:
			b, _ := readBytesFrom(c, sub.Size)
			t.Number = uintFromBytes(b)
		case IDTrackType:
			b, _ := readBytesFrom(c, sub.Size)
			t.Type = uintFromBytes(b)
		case IDCodecID:
			b, _ := readBytesFrom(c, sub.Size)
			t.CodecID = string(b)
		case IDTrackAudio:
			b, _ := readBytesFrom(c, sub.Size)
			parseAudioInfo(b, &t)
		default:
			skipN(c, sub.Size)
		}
	}
	return t
}

func parseAudioInfo(body []byte, t *Track) {
	c := &countingReader{r: bytesReader(body)}
	for c.pos < int64(len(body)) {
		sub, err := readHeaderFrom(c)
		if err != nil {
			break
		}
		switch sub.ID {
		case IDSamplingFreq:
			b, _ := readBytesFrom(c, sub.Size)
			t.Rate = floatFromBytes(b)
		case IDChannels:
			b, _ := readBytesFrom(c, sub.Size)
			t.Channels = uintFromBytes(b)
		default:
			skipN(c, sub.Size)
		}
	}
}

func floatFromBytes(b []byte) float64 {
	bits := uintFromBytes(b)
	switch len(b) {
	case 4:
		return float64(float32frombits(uint32(bits)))
	case 8:
		return float64frombits(bits)
	}
	return 0
}
