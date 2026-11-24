// Package provenance 生成结果清单并用 go-mp4 核验结果结构。
package provenance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediarun/internal/audio"
	"mediarun/internal/ebml"
	"mediarun/internal/rtp"
)

// Manifest 记录来源摘要、处理参数、错误位置与结果摘要。
type Manifest struct {
	SourceSHA256  string         `json:"source_sha256"`
	RTPSourceSHA  string         `json:"rtp_source_sha256"`
	ParamsSHA256  string         `json:"params_sha256"`
	ParamsJSON    string         `json:"params_json"`
	Errors        []*ebml.Error  `json:"errors"`
	AudioStats    []audio.Stats  `json:"audio_stats"`
	RTPStats      rtp.Stats      `json:"rtp_stats"`
	Unrecovered   int            `json:"unrecovered"`
	AVSyncMS      float64        `json:"av_sync_ms"`
	ResultSHA256  string         `json:"result_sha256"`
}

// Write 原子写入清单（临时文件 + rename）。
func Write(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load 读取清单。
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// WriteStructure 写出结果结构文件（ftyp + 含结果摘要的 free box）。
func WriteStructure(path, resultSHA string) error {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, &mp4.Ftyp{
		MajorBrand:   [4]byte{'m', 's', 'd', 'h'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}},
		},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(&buf, &mp4.Free{Data: []byte(resultSHA)}, mp4.Context{}); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// VerifyStructure 用 go-mp4 核验结果结构：必须含 ftyp，且 free 载荷等于结果摘要。
func VerifyStructure(path, expectSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var sawFtyp, sawFree bool
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			sawFtyp = true
		case mp4.BoxTypeFree():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			free, ok := box.(*mp4.Free)
			if !ok {
				return nil, fmt.Errorf("unexpected free box type")
			}
			if string(free.Data) != expectSHA {
				return nil, fmt.Errorf("result structure digest mismatch")
			}
			sawFree = true
		}
		if _, err := h.Expand(); err != nil && err != io.EOF {
			return nil, nil // 叶子盒无需展开
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	if !sawFtyp || !sawFree {
		return fmt.Errorf("result structure incomplete: ftyp=%v free=%v", sawFtyp, sawFree)
	}
	return nil
}
