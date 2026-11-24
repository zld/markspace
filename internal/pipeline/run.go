// Package pipeline 以有界队列串联解析、音频、恢复、定稿各阶段，
// 下游变慢时经通道容量向上游施加背压。
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"mediarun/internal/checkpoint"
	"mediarun/internal/provenance"
)

// ErrInterrupted 表示按 --interrupt-after 在阶段边界中断（退出码 10）。
var ErrInterrupted = errors.New("pipeline interrupted")

// Params 为处理参数；其摘要绑定检查点。
type Params struct {
	Seed      int64   `json:"seed"`
	Rate      int     `json:"rate"`
	Layout    string  `json:"layout"`
	LUFS      float64 `json:"lufs"`
	Loss      float64 `json:"loss"`
	RTPWindow int     `json:"rtp_window"`
	QueueCap  int     `json:"queue_cap"`
}

// Canonical 返回参数的规范 JSON（字段序稳定）。
func (p Params) Canonical() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// SHA256 返回参数摘要。
func (p Params) SHA256() string {
	s := sha256.Sum256([]byte(p.Canonical()))
	return hex.EncodeToString(s[:])
}

// Result 为终端输出的指标 JSON。
type Result struct {
	AVSyncMS           float64 `json:"av_sync_ms"`
	PacketRecoveryRate float64 `json:"packet_recovery_rate"`
	LatencyP95MS       float64 `json:"latency_p95_ms"`
	MemoryPeakBytes    uint64  `json:"memory_peak_bytes"`
	ResultSHA256       string  `json:"result_sha256"`
	Unrecovered        int     `json:"unrecovered"`
	ErrorCount         int     `json:"error_count"`
}

// Runner 执行或续跑流水线。
type Runner struct {
	Params         Params
	WorkDir        string
	MediaPath      string
	RTPPath        string
	MediaSHA       string
	RTPSHA         string
	InterruptAfter string

	memPeak   uint64
	latencies []float64 // 毫秒，定长蓄水池上限 4096
}

func (r *Runner) path(name string) string { return filepath.Join(r.WorkDir, name) }

func (r *Runner) inputSHA() string {
	s := sha256.Sum256([]byte(r.MediaSHA + ":" + r.RTPSHA))
	return hex.EncodeToString(s[:])
}

func (r *Runner) sampleMem() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.HeapAlloc > r.memPeak {
		r.memPeak = m.HeapAlloc
	}
}

func (r *Runner) addLatency(ms float64) {
	if len(r.latencies) < 4096 {
		r.latencies = append(r.latencies, ms)
	}
}

func (r *Runner) p95Latency() float64 {
	if len(r.latencies) == 0 {
		return 0
	}
	s := append([]float64(nil), r.latencies...)
	sort.Float64s(s)
	return s[int(float64(len(s)-1)*0.95)]
}

// Run 执行流水线；存在检查点时按阶段续跑。
func (r *Runner) Run() (*Result, error) {
	if err := os.MkdirAll(r.WorkDir, 0o755); err != nil {
		return nil, err
	}
	store := checkpoint.NewStore(r.path("checkpoint.json"))
	st, err := store.Load()
	if err != nil {
		return nil, err
	}
	if st != nil {
		if err := store.Verify(st, r.inputSHA(), r.Params.SHA256()); err != nil {
			return nil, err
		}
	}
	stage := "init"
	if st != nil {
		stage = st.Stage
	}
	save := func(stage string, offset int64) error {
		return store.Save(&checkpoint.State{
			InputSHA256:  r.inputSHA(),
			ParamsSHA256: r.Params.SHA256(),
			Stage:        stage,
			Offset:       offset,
		})
	}
	switch stage {
	case checkpoint.StageDone:
		return r.resultFromManifest()
	case "init":
		n, err := r.stageParseAudio()
		if err != nil {
			return nil, err
		}
		if err := save(checkpoint.StageAudio, n); err != nil {
			return nil, err
		}
		r.sampleMem()
		if r.InterruptAfter == "audio" {
			return nil, ErrInterrupted
		}
		fallthrough
	case checkpoint.StageAudio:
		if err := r.stageRTP(); err != nil {
			return nil, err
		}
		if err := save(checkpoint.StageRTP, 0); err != nil {
			return nil, err
		}
		r.sampleMem()
		if r.InterruptAfter == "rtp" {
			return nil, ErrInterrupted
		}
		fallthrough
	case checkpoint.StageRTP:
		if err := r.finalize(); err != nil {
			return nil, err
		}
		if err := save(checkpoint.StageDone, 0); err != nil {
			return nil, err
		}
		r.sampleMem()
	default:
		return nil, fmt.Errorf("unknown checkpoint stage %q", stage)
	}
	return r.resultFromManifest()
}

func (r *Runner) resultFromManifest() (*Result, error) {
	m, err := provenance.Load(r.path("manifest.json"))
	if err != nil {
		return nil, err
	}
	r.sampleMem()
	return &Result{
		AVSyncMS:           m.AVSyncMS,
		PacketRecoveryRate: m.RTPStats.RecoveryRate,
		LatencyP95MS:       r.p95Latency(),
		MemoryPeakBytes:    r.memPeak,
		ResultSHA256:       m.ResultSHA256,
		Unrecovered:        m.Unrecovered,
		ErrorCount:         len(m.Errors),
	}, nil
}
