package fixtures

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// videoSizes 生成 24 个视频样本大小，总和精确为 974400 字节。
func videoSizes() []uint32 {
	const total = 974400
	sizes := make([]uint32, 24)
	var sum int64
	for i := range sizes {
		delta := int64((i*7919)%2001) - 1000
		sizes[i] = uint32(40600 + delta)
		sum += int64(sizes[i])
	}
	sizes[len(sizes)-1] += uint32(total - sum)
	return sizes
}

// audioSizes 生成 25 个音频样本大小（总计 25600 字节）。
func audioSizes() []uint32 {
	sizes := make([]uint32, 25)
	for i := range sizes {
		sizes[i] = 1024
	}
	return sizes
}

// mxfVideoTrack 为良好 MXF 的视频轨：旋转 90 + 合法裁剪 + 冲突色彩。
func mxfVideoTrack() MXFTrack {
	return MXFTrack{
		ID: 1, Kind: "video", Name: "mxf-video",
		RateNum: 24, RateDen: 1,
		Width: 96, Height: 54, Rotation: 90,
		Crop:        &[4]uint32{2, 4, 48, 88},
		Color:       &[4]uint16{9, 16, 9, 0},
		AltColor:    &[4]uint16{1, 13, 1, 1},
		SampleDur:   1,
		SampleSizes: videoSizes(),
		Seed:        "mxf-video",
	}
}

// GoodMXF 生成良好 MXF 母版（视频 + 音频 + 辅助元数据轨）。
func GoodMXF() []byte {
	return BuildMXF([]MXFTrack{
		mxfVideoTrack(),
		{ID: 2, Kind: "audio", Name: "mxf-audio",
			RateNum: 48000, RateDen: 1, SampleDur: 1920,
			SampleSizes: audioSizes(), Seed: "mxf-audio"},
		{ID: 3, Kind: "meta", Name: "mxf-meta", RateNum: 24, RateDen: 1},
	})
}

// GoodMP4 生成良好 MP4 母版（非零起点、旋转、裁剪、colr、双轨时基）。
func GoodMP4() []byte {
	return BuildMP4([]MP4Track{
		{ID: 1, Kind: "video", Timescale: 12288,
			Width: 96, Height: 54, Rotation: 90,
			Crop: &[4]uint32{2, 4, 48, 88}, Color: &[4]uint16{1, 13, 1, 1},
			Origin: 512, SampleDelta: 512,
			SampleSizes: videoSizes(), Seed: "mp4-video"},
		{ID: 2, Kind: "audio", Timescale: 48000, SampleDelta: 1920,
			SampleSizes: audioSizes(), Seed: "mp4-audio"},
	})
}

// TruncatedMP4 生成截断 MP4 盒故障样例，返回数据与被截断盒的偏移。
func TruncatedMP4() ([]byte, int64) {
	buf := GoodMP4()
	off := int64(bytes.Index(buf, []byte("mdat")) - 4)
	return buf[:len(buf)-137], off
}

// MXFOverrun 生成 MXF 元数据越界故障样例，返回数据与越界 KLV 的偏移。
func MXFOverrun() ([]byte, int64) {
	header := klv(ulHeaderPartition, make([]byte, 16))
	value := mxfTrackValue(mxfVideoTrack())
	bad := append([]byte{}, ulTrackMeta...)
	bad = append(bad, berLen(len(value)+64)...) // 声明长度超出实际
	bad = append(bad, value...)
	return append(header, bad...), int64(len(header))
}

// CropOOBMP4 生成裁剪越界故障样例（裁剪框超出旋转后画幅）。
func CropOOBMP4() []byte {
	return BuildMP4([]MP4Track{
		{ID: 1, Kind: "video", Timescale: 12288,
			Width: 96, Height: 54, Rotation: 0,
			Crop: &[4]uint32{0, 0, 200, 200}, SampleDelta: 512,
			SampleSizes: videoSizes(), Seed: "oob-video"},
	})
}

// WrapDupPackets 生成时间戳回绕与重复故障样例（RTP 包序列）。
func WrapDupPackets() []*rtp.Packet {
	stamps := []uint32{0xFFFFFFF0, 0x00000010, 0x00000010, 0x00000020}
	pkts := make([]*rtp.Packet, len(stamps))
	for i, ts := range stamps {
		pkts[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      ts,
				SSRC:           7,
			},
			Payload: pseudoBytes("wrap-dup", 16),
		}
	}
	return pkts
}

// Garbage 生成无法识别签名的故障样例。
func Garbage() []byte { return pseudoBytes("garbage", 2048) }

// TooManyTracksMP4 生成轨道数超限故障样例。
func TooManyTracksMP4(n int) []byte {
	tracks := make([]MP4Track, n)
	for i := range tracks {
		tracks[i] = MP4Track{
			ID: i + 1, Kind: "video", Timescale: 90000,
			Width: 16, Height: 16, SampleDelta: 3000,
			SampleSizes: []uint32{16}, Seed: "many",
		}
	}
	return BuildMP4(tracks)
}

// WriteAll 将演示批次的全部样例写入 dir，返回固定顺序的文件路径。
func WriteAll(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := []struct {
		name string
		data []byte
	}{
		{"good.mxf", GoodMXF()},
		{"good.mp4", GoodMP4()},
		{"truncated.mp4", mustTruncated()},
		{"overrun.mxf", mustOverrun()},
		{"crop_oob.mp4", CropOOBMP4()},
		{"garbage.bin", Garbage()},
		{"many_tracks.mp4", TooManyTracksMP4(9)},
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func mustTruncated() []byte { b, _ := TruncatedMP4(); return b }
func mustOverrun() []byte   { b, _ := MXFOverrun(); return b }
