// Package rtp 按序号缺口窗口恢复本地 RTP 样例流。
package rtp

import (
	"fmt"

	"github.com/pion/rtp"
)

// Config 为恢复配置。
type Config struct {
	Window int `json:"window"` // 可恢复的最大连续缺包数
}

// Stats 为恢复统计；超窗缺包计入 Unrecovered，不伪造成功。
type Stats struct {
	Received     int     `json:"received"`
	Recovered    int     `json:"recovered"`
	Unrecovered  int     `json:"unrecovered"`
	RecoveryRate float64 `json:"recovery_rate"`
}

// Recoverer 按序号流式恢复。
type Recoverer struct {
	cfg      Config
	started  bool
	expect   uint16
	last     []byte // 上一交付载荷，用于确定性填充
	received int
	recovered int
	unrecovered int
}

// NewRecoverer 构造恢复器。
func NewRecoverer(cfg Config) *Recoverer {
	return &Recoverer{cfg: cfg}
}

// Stats 返回当前统计与恢复率。
func (r *Recoverer) Stats() Stats {
	total := r.received + r.recovered + r.unrecovered
	rate := 1.0
	if total > 0 {
		rate = float64(r.received+r.recovered) / float64(total)
	}
	return Stats{Received: r.received, Recovered: r.recovered,
		Unrecovered: r.unrecovered, RecoveryRate: rate}
}

// Push 送入一个 RTP 包，返回按序交付的载荷（含恢复的填充包）。
func (r *Recoverer) Push(pkt *rtp.Packet) ([][]byte, error) {
	seq := pkt.SequenceNumber
	if !r.started {
		r.started = true
		r.expect = seq
	}
	if seq < r.expect { // 重复/迟到包：丢弃
		return nil, nil
	}
	var out [][]byte
	if gap := int(seq - r.expect); gap > 0 {
		if gap <= r.cfg.Window {
			// 窗内缺口：以上一载荷确定性填充
			for i := 0; i < gap; i++ {
				fill := make([]byte, len(r.last))
				copy(fill, r.last)
				out = append(out, fill)
			}
			r.recovered += gap
		} else {
			// 超窗缺包：不可恢复，明确计数
			r.unrecovered += gap
		}
	}
	r.received++
	payload := make([]byte, len(pkt.Payload))
	copy(payload, pkt.Payload)
	out = append(out, payload)
	r.last = payload
	r.expect = seq + 1
	return out, nil
}

// UnmarshalPacket 解析一帧 RTP 字节。
func UnmarshalPacket(frame []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(frame); err != nil {
		return nil, fmt.Errorf("rtp unmarshal: %w", err)
	}
	return p, nil
}
