// Package testkit 以确定性方式生成 Matroska 与 RTP 本地样例，
// 覆盖未知长度元素、截断簇、损坏块、缺失时间戳、回绕与超窗丢包。
package testkit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// Inputs 为生成的样例文件及其摘要。
type Inputs struct {
	MediaPath    string `json:"media_path"`
	RTPPath      string `json:"rtp_path"`
	MediaSHA256  string `json:"media_sha256"`
	RTPSHA256    string `json:"rtp_sha256"`
}

// Ensure 返回 dir 下的样例；已存在则不覆盖（续跑时核验来源），否则生成。
func Ensure(dir string, seed int64, loss float64) (*Inputs, error) {
	in := &Inputs{
		MediaPath: filepath.Join(dir, "sample.mkv"),
		RTPPath:   filepath.Join(dir, "stream.rtp"),
	}
	if fileExists(in.MediaPath) && fileExists(in.RTPPath) {
		in.MediaSHA256 = sha256File(in.MediaPath)
		in.RTPSHA256 = sha256File(in.RTPPath)
		return in, nil
	}
	return Generate(dir, seed, loss)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Generate 在 dir 下生成确定性样例；相同 seed/loss 得到相同字节。
func Generate(dir string, seed int64, loss float64) (*Inputs, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(seed))
	in := &Inputs{
		MediaPath: filepath.Join(dir, "sample.mkv"),
		RTPPath:   filepath.Join(dir, "stream.rtp"),
	}
	if err := writeAtomic(in.MediaPath, buildMedia(rng)); err != nil {
		return nil, err
	}
	if err := writeAtomic(in.RTPPath, buildRTP(rng, loss)); err != nil {
		return nil, err
	}
	in.MediaSHA256 = sha256File(in.MediaPath)
	in.RTPSHA256 = sha256File(in.RTPPath)
	return in, nil
}

// --- EBML 构造辅助 ---

func vintID(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	}
	return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
}

func vintSize(n uint64) []byte {
	switch {
	case n < 0x7F:
		return []byte{0x80 | byte(n)}
	case n < 0x3FFF:
		return []byte{0x40 | byte(n>>8), byte(n)}
	}
	return []byte{0x10 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func elem(id uint32, payload []byte) []byte {
	out := vintID(id)
	out = append(out, vintSize(uint64(len(payload)))...)
	return append(out, payload...)
}

func elemUint(id uint32, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	n := 8
	for n > 1 && b[8-n] == 0 {
		n--
	}
	return elem(id, b[8-n:])
}

func elemStr(id uint32, s string) []byte { return elem(id, []byte(s)) }

func elemFloat64(id uint32, f float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(f))
	return elem(id, b[:])
}

func unknownSizeHeader(id uint32) []byte {
	out := vintID(id)
	if id > 0xFFFFFF { // Segment：8 字节全 1
		return append(out, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	}
	return append(out, 0xFF) // Cluster：1 字节全 1
}

func simpleBlock(track uint64, rel int16, payload []byte) []byte {
	var b []byte
	b = append(b, 0x80|byte(track)) // 轨道号 vint（<127）
	var rb [2]byte
	binary.BigEndian.PutUint16(rb[:], uint16(rel))
	b = append(b, rb[:]...)
	b = append(b, 0x80) // keyframe，无 lacing
	b = append(b, payload...)
	return elem(0xA3, b)
}

const (
	audioRate   = 44100
	audioBlockN = 882 // 20ms @44100
	videoEvery  = 40  // ms
	audioEvery  = 20  // ms
)

func audioPCM(blockIdx int) []byte {
	out := make([]byte, 2*audioBlockN)
	for i := 0; i < audioBlockN; i++ {
		n := blockIdx*audioBlockN + i
		v := int16(math.Sin(2*math.Pi*440*float64(n)/audioRate) * 0.3 * 32767)
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}

func videoFrame(rng *rand.Rand) []byte {
	b := make([]byte, 32)
	rng.Read(b)
	return b
}

// interleave 合并音视块为按时间交错的 SimpleBlock 序列。
func interleave(rng *rand.Rand, startMS, endMS int, audioStart int) ([]byte, int) {
	var out []byte
	for t := startMS; t < endMS; t += audioEvery {
		if t%videoEvery == 0 {
			out = append(out, simpleBlock(1, int16(t-startMS), videoFrame(rng))...)
		}
		out = append(out, simpleBlock(2, int16(t-startMS), audioPCM(audioStart))...)
		audioStart++
	}
	return out, audioStart
}

func buildMedia(rng *rand.Rand) []byte {
	var m []byte
	// EBML 头
	var hdr []byte
	hdr = append(hdr, elemUint(0x4286, 1)...) // EBMLVersion
	hdr = append(hdr, elemUint(0x42F7, 1)...) // EBMLReadVersion
	hdr = append(hdr, elemUint(0x42F2, 4)...) // EBMLMaxIDLength
	hdr = append(hdr, elemUint(0x42F3, 8)...) // EBMLMaxSizeLength
	hdr = append(hdr, elemStr(0x4282, "matroska")...)
	hdr = append(hdr, elemUint(0x4287, 4)...)
	hdr = append(hdr, elemUint(0x4285, 2)...)
	m = append(m, elem(0x1A45DFA3, hdr)...)
	// Segment（未知长度）
	m = append(m, unknownSizeHeader(0x18538067)...)
	m = append(m, elem(0x1549A966, elemUint(0x2AD7B1, 1000000))...) // Info: 1ms 刻度
	// Tracks：1 视频 + 2 音频(44100 mono)
	var tracks []byte
	var v []byte
	v = append(v, elemUint(0xD7, 1)...)
	v = append(v, elemUint(0x83, 1)...)
	v = append(v, elemStr(0x86, "V_MEDIARUN_RAW")...)
	tracks = append(tracks, elem(0xAE, v)...)
	var a []byte
	a = append(a, elemUint(0xD7, 2)...)
	a = append(a, elemUint(0x83, 2)...)
	a = append(a, elemStr(0x86, "A_PCM/INT/LIT")...)
	var au []byte
	au = append(au, elemFloat64(0xB5, audioRate)...)
	au = append(au, elemUint(0x9F, 1)...)
	a = append(a, elem(0xE1, au)...)
	tracks = append(tracks, elem(0xAE, a)...)
	m = append(m, elem(0x1654AE6B, tracks)...)
	// Cluster1（定长）：0..1000ms
	body, next := interleave(rng, 0, 1000, 0)
	c1 := append(elemUint(0xE7, 0), body...)
	m = append(m, elem(0x1F43B675, c1)...)
	// Cluster2（未知长度）：1000..2000ms，遇下一个 Cluster ID 结束
	body, next = interleave(rng, 1000, 2000, next)
	c2 := append(elemUint(0xE7, 1000), body...)
	m = append(m, unknownSizeHeader(0x1F43B675)...)
	m = append(m, c2...)
	// Cluster3（定长）：一个好块 + 声明尺寸越过簇尾的损坏块
	c3 := elemUint(0xE7, 2000)
	c3 = append(c3, simpleBlock(2, 0, audioPCM(next))...)
	next++
	// 损坏块：声明 5000 字节，实际载荷仅 10 字节且为簇内最后元素
	corrupt := append(vintID(0xA3), vintSize(5000)...)
	corrupt = append(corrupt, audioPCM(next)[:10]...)
	c3 = append(c3, corrupt...)
	m = append(m, elem(0x1F43B675, c3)...)
	// Cluster4：缺失 Timestamp；含回绕块（rel=-30000）与正常块
	var c4 []byte
	c4 = append(c4, simpleBlock(2, -30000, audioPCM(next))...) // 回绕 → 确定性修正
	next++
	c4 = append(c4, simpleBlock(1, 0, videoFrame(rng))...)
	c4 = append(c4, simpleBlock(2, 20, audioPCM(next))...)
	next++
	c4 = append(c4, simpleBlock(1, 40, videoFrame(rng))...)
	c4 = append(c4, simpleBlock(2, 40, audioPCM(next))...)
	m = append(m, elem(0x1F43B675, c4)...)
	// Cluster5：声明尺寸远超文件剩余 → 截断簇
	m = append(m, vintID(0x1F43B675)...)
	m = append(m, vintSize(100000)...)
	m = append(m, make([]byte, 40)...)
	return m
}

func buildRTP(rng *rand.Rand, loss float64) []byte {
	var out []byte
	payload := make([]byte, 160)
	rng.Read(payload) // 固定载荷模式，确定性
	for seq := 0; seq < 500; seq++ {
		drop := seq == 100 || (seq >= 300 && seq < 312) // 窗内 1 个 + 超窗 12 个
		if !drop && rng.Float64() < loss {
			drop = true
		}
		if drop {
			continue
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{Version: 2, SequenceNumber: uint16(seq),
				Timestamp: uint32(seq * 160), SSRC: 0x1234},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		var ln [2]byte
		binary.BigEndian.PutUint16(ln[:], uint16(len(raw)))
		out = append(out, ln[:]...)
		out = append(out, raw...)
	}
	return out
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sha256File(path string) string {
	b, _ := os.ReadFile(path)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
