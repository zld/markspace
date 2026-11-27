// Package pipeline drives the rebuild stages in order, persists a
// checkpoint after each completed stage, and resumes from the last
// complete checkpoint on restart.
package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediarebuild/internal/fragment"
	"mediarebuild/internal/ingest"
	"mediarebuild/internal/model"
	mp4scan "mediarebuild/internal/mp4"
	"mediarebuild/internal/rtcp"
	"mediarebuild/internal/rtp"
	"mediarebuild/internal/sample"
	"mediarebuild/internal/track"
)

// Stage names in execution order.
const (
	StageIngest   = "ingest"
	StageRTP      = "rtp"
	StageRTCP     = "rtcp"
	StageScan     = "scan"
	StageValidate = "validate"
	StageFragment = "fragment"
	StageMedia    = "media"
	StageMerge    = "merge"
)

// StageOrder is the fixed pipeline order.
var StageOrder = []string{
	StageIngest, StageRTP, StageRTCP, StageScan,
	StageValidate, StageFragment, StageMedia, StageMerge,
}

// FileStat summarises one ingested record stream.
type FileStat struct {
	Name    string `json:"name"`
	Records int    `json:"records"`
	Bytes   int64  `json:"bytes"`
}

// IngestResult is the ingest stage outcome.
type IngestResult struct {
	Files []FileStat  `json:"files"`
	Diags model.Diags `json:"diags"`
}

// RTPStreamStat summarises one reordered RTP stream.
type RTPStreamStat struct {
	Name    string `json:"name"`
	Packets int    `json:"packets"`
	Unique  int    `json:"unique"`
	Dups    int    `json:"dups"`
	Lost    int    `json:"lost"`
}

// RTPResult is the rtp stage outcome.
type RTPResult struct {
	Streams []RTPStreamStat `json:"streams"`
	Diags   model.Diags     `json:"diags"`
}

// RTCPResult is the rtcp stage outcome.
type RTCPResult struct {
	AlignmentErrorNanos int64       `json:"alignment_error_nanos"`
	Diags               model.Diags `json:"diags"`
}

// ScanResult is the scan stage outcome (per container).
type ScanResult struct {
	Files map[string]mp4scan.Result `json:"files"`
}

// ValidateResult is the validate stage outcome.
type ValidateResult struct {
	Files map[string]track.Result `json:"files"`
}

// FragmentResult is the fragment stage outcome.
type FragmentResult struct {
	Files map[string]fragment.Result `json:"files"`
}

// MediaResult holds off-screen media measurements.
type MediaResult struct {
	FrameHashes   []string `json:"frame_hashes"`
	FragmentHash  string   `json:"fragment_hash"`
	PixelMean     float64  `json:"pixel_mean"`
	PixelPeak     int      `json:"pixel_peak"`
	AudioPeak     int      `json:"audio_peak"`
	AudioPeakDBFS float64  `json:"audio_peak_dbfs"`
}

// MergeResult is the merge stage outcome.
type MergeResult struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	Entries        int    `json:"entries"`
}

// Results accumulates every stage outcome; it is what merge consumes.
type Results struct {
	Ingest   *IngestResult   `json:"ingest,omitempty"`
	RTP      *RTPResult      `json:"rtp,omitempty"`
	RTCP     *RTCPResult     `json:"rtcp,omitempty"`
	Scan     *ScanResult     `json:"scan,omitempty"`
	Validate *ValidateResult `json:"validate,omitempty"`
	Fragment *FragmentResult `json:"fragment,omitempty"`
	Media    *MediaResult    `json:"media,omitempty"`
	Merge    *MergeResult    `json:"merge,omitempty"`
}

// AllDiags flattens diagnostics across stages.
func (r *Results) AllDiags() model.Diags {
	var out model.Diags
	add := func(d model.Diags) { out = append(out, d...) }
	if r.Ingest != nil {
		add(r.Ingest.Diags)
	}
	if r.RTP != nil {
		add(r.RTP.Diags)
	}
	if r.RTCP != nil {
		add(r.RTCP.Diags)
	}
	if r.Scan != nil {
		for _, f := range r.Scan.Files {
			add(f.Diags)
		}
	}
	if r.Validate != nil {
		for _, f := range r.Validate.Files {
			add(f.Diags)
		}
	}
	if r.Fragment != nil {
		for _, f := range r.Fragment.Files {
			add(f.Diags)
		}
	}
	return out
}

// recordFiles are the generated record streams, in stable order.
var recordFiles = []string{"rtp-clean.bin", "rtp-lossy.bin", "rtcp.bin"}

// containerFiles are the generated MP4 samples, in stable order.
var containerFiles = []string{
	"normal.mp4", "clean-frag.mp4", "truncated.mp4",
	"corrupt-frag.mp4", "incomplete-index.mp4",
}

// fragmentFiles are the containers checked by the fragment stage.
var fragmentFiles = []string{"clean-frag.mp4", "corrupt-frag.mp4"}

// runIngest reads every record stream, counting records and bytes.
// Corrupt or truncated records surface as diagnostics, never silent skips.
func runIngest(ctx context.Context, sampleDir string) (*IngestResult, error) {
	res := &IngestResult{}
	for _, name := range recordFiles {
		data, err := os.ReadFile(filepath.Join(sampleDir, name))
		if err != nil {
			return nil, err
		}
		rd := ingest.NewReader(bytes.NewReader(data), 0)
		st := FileStat{Name: name, Bytes: int64(len(data))}
		for {
			rec, err := rd.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if d, ok := err.(model.Diag); ok {
					res.Diags.Add(d)
					break // stream is unusable past the first bad record
				}
				return nil, err
			}
			_ = rec
			st.Records++
		}
		res.Files = append(res.Files, st)
	}
	return res, nil
}

// runRTP reorders each RTP record stream within a fixed wait window.
func runRTP(ctx context.Context, sampleDir string) (*RTPResult, error) {
	res := &RTPResult{}
	for _, name := range []string{"rtp-clean.bin", "rtp-lossy.bin"} {
		data, err := os.ReadFile(filepath.Join(sampleDir, name))
		if err != nil {
			return nil, err
		}
		rd := ingest.NewReader(bytes.NewReader(data), 0)
		re := rtp.NewReorderer(8)
		for {
			rec, err := rd.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				if d, ok := err.(model.Diag); ok {
					res.Diags.Add(d)
					break
				}
				return nil, err
			}
			if d := re.Push(rec.Payload, rec.Offset); d != nil {
				res.Diags.Add(*d)
			}
			re.Drain(false)
		}
		re.Drain(true)
		st := re.Stats()
		res.Streams = append(res.Streams, RTPStreamStat{
			Name:    name,
			Packets: st.Received,
			Unique:  st.Unique,
			Dups:    st.Dups,
			Lost:    st.Lost,
		})
	}
	return res, nil
}

// runRTCP builds the track mapper from sender-report records and maps
// one event per video frame onto the unified timeline.
func runRTCP(ctx context.Context, sampleDir string) (*RTCPResult, error) {
	res := &RTCPResult{}
	data, err := os.ReadFile(filepath.Join(sampleDir, "rtcp.bin"))
	if err != nil {
		return nil, err
	}
	rd := ingest.NewReader(bytes.NewReader(data), 0)
	mapper := rtcp.NewMapper()
	for {
		rec, err := rd.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if d, ok := err.(model.Diag); ok {
				res.Diags.Add(d)
				break
			}
			return nil, err
		}
		if len(rec.Payload) != 16 {
			res.Diags.Add(model.NewDiag(model.StageRTCP, "bad-anchor", rec.Offset, false,
				fmt.Sprintf("anchor record is %d bytes, want 16", len(rec.Payload))))
			continue
		}
		ssrc := binary.BigEndian.Uint32(rec.Payload[0:4])
		ntp := int64(binary.BigEndian.Uint64(rec.Payload[4:12]))
		rts := binary.BigEndian.Uint32(rec.Payload[12:16])
		mapper.AddSenderReport(ssrc, ntp, rts)
	}
	var maxErr int64
	for _, ts := range sample.VideoEvents(sample.VideoFrames) {
		mt, err := mapper.Map(sample.VideoSSRC, sample.VideoClock, ts)
		if err != nil {
			if d, ok := err.(model.Diag); ok {
				res.Diags.Add(d)
				continue
			}
			return nil, err
		}
		diff := int64(mt) - sample.ExpectedMediaTimeNanos(ts)
		if diff < 0 {
			diff = -diff
		}
		if diff > maxErr {
			maxErr = diff
		}
	}
	res.AlignmentErrorNanos = maxErr
	return res, nil
}

// runScan scans every container sample.
func runScan(ctx context.Context, sampleDir string) (*ScanResult, error) {
	res := &ScanResult{Files: map[string]mp4scan.Result{}}
	for _, name := range containerFiles {
		r, err := mp4scan.Scan(ctx, filepath.Join(sampleDir, name))
		if err != nil {
			return nil, err
		}
		res.Files[name] = r
	}
	return res, nil
}

// runValidate validates tracks in every container sample.
func runValidate(ctx context.Context, sampleDir string) (*ValidateResult, error) {
	res := &ValidateResult{Files: map[string]track.Result{}}
	for _, name := range containerFiles {
		r, err := track.Validate(ctx, filepath.Join(sampleDir, name))
		if err != nil {
			return nil, err
		}
		res.Files[name] = r
	}
	return res, nil
}

// runFragment checks fragment boundaries of the fragmented samples.
func runFragment(ctx context.Context, sampleDir string) (*FragmentResult, error) {
	res := &FragmentResult{Files: map[string]fragment.Result{}}
	for _, name := range fragmentFiles {
		r, err := fragment.Check(ctx, filepath.Join(sampleDir, name), []uint32{1})
		if err != nil {
			return nil, err
		}
		res.Files[name] = r
	}
	return res, nil
}

// runMedia computes off-screen media measurements: per-frame hashes,
// the clean fragment's mdat hash, pixel statistics and loudness peak.
func runMedia(ctx context.Context, sampleDir string, seed int64) (*MediaResult, error) {
	res := &MediaResult{}
	frames := sample.Frames(seed)
	for _, f := range frames {
		h := sha256.Sum256(f)
		res.FrameHashes = append(res.FrameHashes, hex.EncodeToString(h[:]))
	}
	stats := sample.ComputePixelStats(frames)
	res.PixelMean = stats.Mean
	res.PixelPeak = stats.Peak
	peak, db := sample.LoudnessPeak(sample.AudioPCM(seed))
	res.AudioPeak = peak
	res.AudioPeakDBFS = db
	// Fragment hash: payload of the first mdat in clean-frag.mp4.
	scan, err := mp4scan.Scan(ctx, filepath.Join(sampleDir, "clean-frag.mp4"))
	if err != nil {
		return nil, err
	}
	mdats := scan.Find("mdat")
	if len(mdats) == 0 {
		return nil, fmt.Errorf("clean-frag.mp4 has no mdat")
	}
	data, err := os.ReadFile(filepath.Join(sampleDir, "clean-frag.mp4"))
	if err != nil {
		return nil, err
	}
	payload := data[mdats[0].Offset+8 : mdats[0].End()]
	h := sha256.Sum256(payload)
	res.FragmentHash = hex.EncodeToString(h[:])
	return res, nil
}
