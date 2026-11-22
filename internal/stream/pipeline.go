package stream

import (
	"context"
	"sync"
	"time"

	"github.com/pion/rtp"

	"mq/internal/timeline"
)

// Options 为流水线参数。
type Options struct {
	QueueCapacity int           // 各级有界队列容量
	SlowConsumer  time.Duration // 模拟慢消费者的每包处理耗时
}

// Result 汇总流水线运行结果，取消后仍保留已确认输出边界。
type Result struct {
	Confirmed   int     // 已确认（消费完成）的包数
	PeakQueue   int64   // 各级队列峰值占用的最大值
	Duplicates  []int   // 重复时间戳的包序号
	Timestamps  []int64 // 解绕后的 64 位时间戳（90kHz）
	Err         error
}

// SourceFunc 产出一个 RTP 包；返回 io.EOF 风格的 done=true 表示结束。
type SourceFunc func(yield func(pkt *rtp.Packet) error) error

// unwrapper 将 32 位 RTP 时间戳扩展为单调 64 位，处理回绕。
type unwrapper struct {
	cycles int64
	last   uint32
	init   bool
}

// Extend 返回扩展后的 64 位时间戳。
func (u *unwrapper) Extend(ts uint32) int64 {
	if !u.init {
		u.init = true
		u.last = ts
		return int64(ts)
	}
	if ts < u.last && u.last-ts > 1<<31 {
		u.cycles += 1 << 32
	}
	u.last = ts
	return u.cycles + int64(ts)
}

// Run 执行三级流水线：打包 -> 时间戳解绕/校验 -> 消费。
// 各级间以有界队列传递；慢消费者触发背压；取消后回收全部 goroutine，
// 并保留已确认输出边界（Confirmed 与 Timestamps）。
func Run(ctx context.Context, src SourceFunc, opts Options) *Result {
	res := &Result{}
	q1 := NewQueue[[]byte](opts.QueueCapacity)
	q2 := NewQueue[*rtp.Packet](opts.QueueCapacity)

	var wg sync.WaitGroup
	var mu sync.Mutex
	setErr := func(err error) {
		mu.Lock()
		if res.Err == nil {
			res.Err = err
		}
		mu.Unlock()
	}

	// 第一级：从源取包并序列化。
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer q1.Close()
		err := src(func(pkt *rtp.Packet) error {
			raw, err := pkt.Marshal()
			if err != nil {
				return err
			}
			return q1.Send(ctx, raw)
		})
		if err != nil && err != context.Canceled && err != ErrClosed {
			setErr(err)
		}
	}()

	// 第二级：解包、时间戳解绕与单调性/重复校验。
	uw := &unwrapper{}
	var lastTS int64 = -1
	idx := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer q2.Close()
		for {
			raw, ok := q1.Recv(ctx)
			if !ok {
				return
			}
			var pkt rtp.Packet
			if err := pkt.Unmarshal(raw); err != nil {
				setErr(timeline.NewErr(timeline.StageStream, "", -1,
					"rtp unmarshal: %v", err))
				return
			}
			ts := uw.Extend(pkt.Header.Timestamp)
			mu.Lock()
			if ts < lastTS {
				mu.Unlock()
				setErr(timeline.NewErr(timeline.StageStream, "", -1,
					"timestamp regression at packet %d: %d < %d", idx, ts, lastTS))
				return
			}
			if ts == lastTS {
				res.Duplicates = append(res.Duplicates, idx)
			}
			lastTS = ts
			res.Timestamps = append(res.Timestamps, ts)
			idx++
			mu.Unlock()
			if err := q2.Send(ctx, &pkt); err != nil {
				return
			}
		}
	}()

	// 第三级：消费（可模拟慢消费者），确认输出边界。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, ok := q2.Recv(ctx)
			if !ok {
				return
			}
			if opts.SlowConsumer > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(opts.SlowConsumer):
				}
			}
			mu.Lock()
			res.Confirmed++
			mu.Unlock()
		}
	}()

	wg.Wait()
	res.PeakQueue = max(q1.Peak(), q2.Peak())
	return res
}

// SamplesSource 由合成时间线的视频样本构造 RTP 包源（90kHz 时钟）。
func SamplesSource(tl *timeline.Timeline, asset *timeline.Asset) SourceFunc {
	samples := tl.VideoSamples()
	return func(yield func(pkt *rtp.Packet) error) error {
		for _, s := range samples {
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(s.Index),
					Timestamp:      uint32(s.Time.Units),
					SSRC:           uint32(s.Index + 1),
				},
				Payload: asset.SampleBytes(timeline.SampleMeta{
					Index: s.Index, Size: s.Size, Offset: s.Offset,
				}),
			}
			if err := yield(pkt); err != nil {
				return err
			}
		}
		return nil
	}
}
