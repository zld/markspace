package ebml

import (
	"fmt"
	"math"
)

func float32frombits(b uint32) float32 { return math.Float32frombits(b) }
func float64frombits(b uint64) float64 { return math.Float64frombits(b) }

// Clock 为统一媒体时基：把（簇基准 + 块相对时间戳）× 刻度
// 换算为纳秒，并对缺失时间戳与回绕做确定性修正。
// 簇基准在进入新簇（ClusterSeq 变化）时冻结：有簇时间戳则取之，
// 否则沿用此前所有簇的最大结束位置，簇内不再漂移。
type Clock struct {
	Scale   uint64 // 纳秒/刻度，默认 1_000_000（1ms）
	lastAbs map[uint64]int64
	curSeq  int
	base    int64 // 当前簇基准（刻度）
	maxEnd  int64 // 各簇 base+rel+1 的最大值（刻度）
	started bool
	errs    []*Error
}

// NewClock 构造统一时基。
func NewClock(scale uint64) *Clock {
	if scale == 0 {
		scale = 1000000
	}
	return &Clock{Scale: scale, lastAbs: map[uint64]int64{}}
}

// Errors 返回修正过程中记录的错误（共享错误模型）。
func (c *Clock) Errors() []*Error { return c.errs }

// Abs 计算块的统一时基纳秒时间戳；必要时原地修正并记录错误。
func (c *Clock) Abs(b *Block) int64 {
	if !c.started || b.ClusterSeq != c.curSeq {
		c.curSeq = b.ClusterSeq
		switch {
		case b.HasClusTS:
			c.base = b.ClusterTS
		case c.started:
			c.base = c.maxEnd // 缺失簇时间戳：确定性沿用此前最大结束位置
		default:
			c.base = 0
		}
		c.started = true
	}
	abs := (c.base + int64(b.RelTS)) * int64(c.Scale)
	if last, ok := c.lastAbs[b.Track]; ok && abs < last {
		// 回绕/乱序：确定性修正为严格单调（上一刻 +1 刻度）
		c.errs = append(c.errs, &Error{
			Code:   CodeTimestampWrap,
			Offset: b.Offset,
			Msg: fmt.Sprintf("track %d timestamp %dns regresses below %dns; corrected monotonic",
				b.Track, abs, last),
		})
		abs = last + int64(c.Scale)
	}
	c.lastAbs[b.Track] = abs
	if end := c.base + int64(b.RelTS) + 1; end > c.maxEnd {
		c.maxEnd = end
	}
	b.AbsTS = abs
	return abs
}
