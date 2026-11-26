package fixture

import (
	"fmt"
	"path/filepath"
)

func buildNormal(s *Spec) error {
	for i := 0; i < 4; i++ {
		video := filepath.Join(s.Root, "video", fmt.Sprintf("seg-%d.m4s", i))
		if err := WriteFragment(video, FragmentSpec{
			Sequence: uint32(100 + i), TrackID: 1, BaseTicks: uint64(i * 90000),
			Samples: []MP4Sample{{Duration: 45000, Size: 24}, {Duration: 45000, Size: 24}},
		}); err != nil {
			return err
		}

		audioOffset := uint64(0)
		if i >= 2 {
			audioOffset = 240 // deterministic 5 ms source-switch clock offset at 48 kHz
		}
		audio := filepath.Join(s.Root, "audio", fmt.Sprintf("seg-%d.m4s", i))
		if err := WriteFragment(audio, FragmentSpec{
			Sequence: uint32(200 + i), TrackID: 2, BaseTicks: uint64(i*48000) + audioOffset,
			Samples: []MP4Sample{{Duration: 24000, Size: 16}, {Duration: 24000, Size: 16}},
		}); err != nil {
			return err
		}
	}
	if err := write(filepath.Join(s.Root, "video.m3u8"), mediaPlaylist("video", 100)); err != nil {
		return err
	}
	if err := write(filepath.Join(s.Root, "audio.m3u8"), mediaPlaylist("audio", 200)); err != nil {
		return err
	}
	master := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="main",NAME="Program Audio",DEFAULT=YES,URI="audio.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=2000000,CODECS="avc1.64001f,mp4a.40.2",AUDIO="main"
video.m3u8
`
	s.Entry = filepath.Join(s.Root, "master.m3u8")
	if err := write(s.Entry, master); err != nil {
		return err
	}
	s.Catalog = filepath.Join(s.Root, "catalog-v1.json")
	catalog := `{
  "version": 1,
  "format": "hls",
  "playlist": "master.m3u8",
  "media_sequence": 100,
  "items": [
    {"id":"v0","uri":"video/seg-0.m4s","track":"video","duration_ns":1000000000},
    {"id":"a0","uri":"audio/seg-0.m4s","track":"audio","duration_ns":1000000000}
  ]
}`
	if err := write(s.Catalog, catalog); err != nil {
		return err
	}
	rtpFix, err := buildCleanRTP(s.Root)
	if err != nil {
		return err
	}
	s.RTPPath = rtpFix.Path
	s.Recovered = rtpFix.Recovered
	s.Lost = rtpFix.Lost
	s.Unrecoverable = rtpFix.Unrecoverable
	s.Format = "hls"
	s.ExpectedFrames = 8
	return nil
}

func mediaPlaylist(kind string, sequence uint64) string {
	var b []byte
	b = append(b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n"...)
	b = append(b, fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", sequence)...)
	for i := 0; i < 4; i++ {
		if i == 2 {
			b = append(b, "#EXT-X-DISCONTINUITY\n"...)
		}
		b = append(b, "#EXTINF:1.000000,\n"...)
		b = append(b, fmt.Sprintf("%s/seg-%d.m4s\n", kind, i)...)
	}
	b = append(b, "#EXT-X-ENDLIST\n"...)
	return string(b)
}
