package pipeline

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"sort"
	"time"

	"mediarun/internal/audio"
	"mediarun/internal/ebml"
)

// metaFile 为解析+音频阶段的落盘元数据。
type metaFile struct {
	Tracks   []ebml.Track   `json:"tracks"`
	Scale    uint64         `json:"scale"`
	Audio    []audio.Stats  `json:"audio_stats"`
	AVSyncMS float64        `json:"av_sync_ms"`
	Errors   []*ebml.Error  `json:"errors"`
	Blocks   int64          `json:"blocks"`
}

// blockSink 在解析协程内做时钟换算与有界窗口排序，经通道向下游施加背压。
type blockSink struct {
	clk    *ebml.Clock
	window []ebml.Block
	cap    int
	out    chan<- ebml.Block
	errs   []*ebml.Error
	tracks []ebml.Track
}

func (s *blockSink) OnInfo(scale uint64)      { s.clk = ebml.NewClock(scale) }
func (s *blockSink) OnTracks(t []ebml.Track)  { s.tracks = t }
func (s *blockSink) OnError(e *ebml.Error)    { s.errs = append(s.errs, e) }

func (s *blockSink) OnBlock(b ebml.Block) {
	s.clk.Abs(&b)
	s.window = append(s.window, b)
	if len(s.window) >= 2*s.cap {
		s.flush(s.cap)
	}
}

func (s *blockSink) flush(n int) {
	sort.SliceStable(s.window, func(i, j int) bool {
		a, b := s.window[i], s.window[j]
		if a.AbsTS != b.AbsTS {
			return a.AbsTS < b.AbsTS
		}
		return a.Track < b.Track
	})
	for i := 0; i < n && i < len(s.window); i++ {
		s.out <- s.window[i]
	}
	s.window = s.window[n:]
}

func (s *blockSink) close() {
	s.flush(len(s.window))
	s.errs = append(s.errs, s.clk.Errors()...)
	close(s.out)
}

// avSync 在线计算音画时间戳最大偏差（毫秒）。
type avSync struct {
	lastAudio int64
	hasAudio  bool
	pending   []int64
	maxDiff   int64
}

func (a *avSync) onAudio(ts int64) {
	if a.hasAudio {
		keep := a.pending[:0]
		for _, v := range a.pending {
			if v <= ts {
				d := v - a.lastAudio
				if ts-v < d {
					d = ts - v
				}
				if d > a.maxDiff {
					a.maxDiff = d
				}
			} else {
				keep = append(keep, v)
			}
		}
		a.pending = keep
	}
	a.lastAudio, a.hasAudio = ts, true
}

func (a *avSync) onVideo(ts int64) {
	if a.hasAudio && ts <= a.lastAudio {
		if d := a.lastAudio - ts; d > a.maxDiff {
			a.maxDiff = d
		}
		return
	}
	a.pending = append(a.pending, ts)
}

func (a *avSync) finish() {
	for _, v := range a.pending {
		if d := v - a.lastAudio; a.hasAudio && d > a.maxDiff {
			a.maxDiff = d
		}
	}
}

// audioAccum 为单条音频轨的流式转换状态。
type audioAccum struct {
	track    ebml.Track
	rs       *audio.Resampler
	raw      *os.File
	sumsq    float64
	inSamp   int
	outSamp  int
	peakIn   float64
	count    int64
}

// stageParseAudio 解析媒体文件并流式完成重采样与声道归一（响度增益待第二遍）。
func (r *Runner) stageParseAudio() (int64, error) {
	f, err := os.Open(r.MediaPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, _ := f.Stat()
	out := make(chan ebml.Block, r.Params.QueueCap) // 有界队列：背压
	sink := &blockSink{cap: r.Params.QueueCap * 4, out: out}
	parseErr := make(chan error, 1)
	go func() {
		parseErr <- ebml.NewParser(f, fi.Size()).Parse(sink)
		sink.close()
	}()

	idx, err := os.Create(r.path("blocks.jsonl"))
	if err != nil {
		return 0, err
	}
	defer idx.Close()
	layout, err := audio.ParseLayout(r.Params.Layout)
	if err != nil {
		return 0, err
	}
	accs := map[uint64]*audioAccum{}
	var av avSync
	var nBlocks int64
	trackType := map[uint64]uint64{}
	getAcc := func(b ebml.Block) (*audioAccum, error) {
		if a, ok := accs[b.Track]; ok {
			return a, nil
		}
		var tr ebml.Track
		for _, t := range sink.tracks {
			if t.Number == b.Track {
				tr = t
			}
		}
		raw, err := os.Create(r.path(fmt.Sprintf("audio1-%d.raw", b.Track)))
		if err != nil {
			return nil, err
		}
		a := &audioAccum{track: tr, raw: raw,
			rs: audio.NewResampler(int(tr.Rate), r.Params.Rate, int(tr.Channels))}
		accs[b.Track] = a
		return a, nil
	}
	for b := range out {
		start := time.Now()
		nBlocks++
		rec, _ := json.Marshal(map[string]any{
			"track": b.Track, "abs_ts": b.AbsTS, "offset": b.Offset,
			"len": len(b.Data), "crc": crc32.ChecksumIEEE(b.Data),
		})
		idx.Write(append(rec, '\n'))
		t, known := trackType[b.Track]
		if !known {
			for _, tr := range sink.tracks {
				trackType[tr.Number] = tr.Type
			}
			t = trackType[b.Track]
		}
		switch t {
		case 2: // 音频轨
			a, err := getAcc(b)
			if err != nil {
				return 0, err
			}
			pcm := make([]int16, len(b.Data)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(b.Data[2*i:]))
			}
			if p := audio.Peak(pcm); p > a.peakIn {
				a.peakIn = p
			}
			a.inSamp += len(pcm) / int(a.track.Channels)
			res := a.rs.Process(pcm)
			res = audio.MapChannels(res, int(a.track.Channels), layout)
			for _, s := range res {
				v := float64(s) / 32768.0
				a.sumsq += v * v
			}
			a.outSamp += len(res) / layout.Channels()
			buf := make([]byte, 2*len(res))
			for i, s := range res {
				binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
			}
			a.raw.Write(buf)
			av.onAudio(b.AbsTS)
		case 1: // 视频轨
			av.onVideo(b.AbsTS)
		}
		r.addLatency(float64(time.Since(start).Microseconds()) / 1000)
	}
	if err := <-parseErr; err != nil {
		return 0, err // 致命解析失败（退出码 3 由入口映射）
	}
	av.finish()
	var stats []audio.Stats
	for _, a := range accs {
		flush := a.rs.Flush()
		flush = audio.MapChannels(flush, int(a.track.Channels), layout)
		a.outSamp += len(flush) / layout.Channels()
		buf := make([]byte, 2*len(flush))
		for i, s := range flush {
			binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
		}
		a.raw.Write(buf)
		a.raw.Close()
		st, err := r.applyLoudness(a, layout)
		if err != nil {
			return 0, err
		}
		stats = append(stats, st)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Track < stats[j].Track })
	meta := &metaFile{Tracks: sink.tracks, Audio: stats,
		AVSyncMS: float64(av.maxDiff) / 1e6, Errors: sink.errs, Blocks: nBlocks}
	if err := writeJSONAtomic(r.path("meta.json"), meta); err != nil {
		return 0, err
	}
	return nBlocks, nil
}
