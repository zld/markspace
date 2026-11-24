// Package checkpoint 落盘续跑状态：绑定输入摘要、参数摘要与阶段偏移。
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrMismatch 表示输入或参数摘要与检查点不一致（退出码 2）。
var ErrMismatch = errors.New("checkpoint digest mismatch")

// 阶段名。
const (
	StageParsed = "parsed"
	StageAudio  = "audio"
	StageRTP    = "rtp"
	StageDone   = "done"
)

// State 为检查点状态。
type State struct {
	InputSHA256  string            `json:"input_sha256"`
	ParamsSHA256 string            `json:"params_sha256"`
	Stage        string            `json:"stage"`
	Offset       int64             `json:"offset"` // 阶段偏移：已处理块数
	Artifacts    map[string]string `json:"artifacts,omitempty"`
}

// Store 管理单个检查点文件。
type Store struct{ Path string }

// NewStore 构造存储。
func NewStore(path string) *Store { return &Store{Path: path} }

// Save 原子写入（临时文件 + rename）。
func (s *Store) Save(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Load 读取检查点；不存在时返回 nil, nil。
func (s *Store) Load() (*State, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint: %w", err)
	}
	return &st, nil
}

// Verify 校验输入与参数摘要；不匹配返回 ErrMismatch。
func (s *Store) Verify(st *State, inputSHA, paramsSHA string) error {
	if st.InputSHA256 != inputSHA {
		return fmt.Errorf("%w: input %s != %s", ErrMismatch, st.InputSHA256, inputSHA)
	}
	if st.ParamsSHA256 != paramsSHA {
		return fmt.Errorf("%w: params changed", ErrMismatch)
	}
	return nil
}

// Clear 删除检查点。
func (s *Store) Clear() error {
	err := os.Remove(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Dir 返回检查点所在目录。
func (s *Store) Dir() string { return filepath.Dir(s.Path) }
