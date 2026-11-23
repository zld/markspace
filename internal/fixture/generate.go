// Package fixture generates synthetic CMAF/fMP4 fragment groups and fault
// artifacts for tests and demos. All output is deterministic.
package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"mediapack/internal/batch"
	"mediapack/internal/model"
)

// Fault names accepted by Config.Inject.
const (
	FaultGap          = "gap"
	FaultTfdtBackstep = "tfdt-backstep"
	FaultTruncatedBox = "truncated-box"
	FaultMissingTfdt  = "missing-tfdt"
	FaultDupMfhd      = "dup-mfhd"
	FaultPartialIndex = "partial-index"
)

// Config describes a synthetic fragment group.
type Config struct {
	Dir       string // input directory to create
	Tracks    int
	Fragments int // total fragments across all tracks
	Inject    []string
}

func has(inject []string, name string) bool {
	for _, s := range inject {
		if s == name {
			return true
		}
	}
	return false
}

// TrackDesc returns the deterministic description of track id (1-based).
func TrackDesc(id int) model.TrackDesc {
	if id%2 == 1 {
		return model.TrackDesc{
			ID: uint32(id), Type: model.MediaVideo, Timescale: 90000,
			Color: model.ColorDesc{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"},
		}
	}
	layout := model.LayoutStereo
	if id == 4 {
		layout = model.Layout51
	}
	return model.TrackDesc{
		ID: uint32(id), Type: model.MediaAudio, Timescale: 48000,
		SampleRate: 48000, Layout: layout,
	}
}

// FragOpts parameterizes BuildFragment.
type FragOpts struct {
	Mfhd      uint32
	Tfdt      uint64
	HasTfdt   bool
	Keyframe  bool
	Duration  uint32 // per-sample duration; 0 omits durations (underivable)
	Seed      []byte
}

func boxHeader(typ string, size int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(size))
	copy(b[4:], typ)
	return b
}

// BuildFragment serializes one deterministic moof+mdat fragment.
func BuildFragment(track model.TrackDesc, o FragOpts) ([]byte, error) {
	flags := uint32(0x02000000) // sync sample
	if !o.Keyframe {
		flags = 0x00010000 // non-sync
	}
	size := uint32(24)
	if len(o.Seed) > 0 {
		size += uint32(o.Seed[0]) % 40
	}
	entry := mp4.TrunEntry{SampleSize: size, SampleFlags: flags}
	trunFlags := uint32(0x200 | 0x400)
	if o.Duration > 0 {
		trunFlags |= 0x100
		entry.SampleDuration = o.Duration
	}
	trun := &mp4.Trun{SampleCount: 1, Entries: []mp4.TrunEntry{entry}}
	trun.SetVersion(0)
	trun.SetFlags(trunFlags)
	tfhd := &mp4.Tfhd{TrackID: track.ID}
	tfhd.SetFlags(0)
	mfhd := &mp4.Mfhd{SequenceNumber: o.Mfhd}

	var traf bytes.Buffer
	for _, box := range []mp4.IImmutableBox{tfhd} {
		if _, err := mp4.Marshal(&traf, box, mp4.Context{}); err != nil {
			return nil, err
		}
	}
	if o.HasTfdt {
		tfdt := &mp4.Tfdt{BaseMediaDecodeTimeV1: o.Tfdt}
		tfdt.SetVersion(1)
		if _, err := mp4.Marshal(&traf, tfdt, mp4.Context{}); err != nil {
			return nil, err
		}
	}
	if _, err := mp4.Marshal(&traf, trun, mp4.Context{}); err != nil {
		return nil, err
	}
	var mfhdBuf bytes.Buffer
	if _, err := mp4.Marshal(&mfhdBuf, mfhd, mp4.Context{}); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	moofSize := 8 + mfhdBuf.Len() + 8 + traf.Len()
	out.Write(boxHeader("moof", moofSize))
	out.Write(mfhdBuf.Bytes())
	out.Write(boxHeader("traf", 8+traf.Len()))
	out.Write(traf.Bytes())
	payload := make([]byte, size)
	for i := range payload {
		if len(o.Seed) > 0 {
			payload[i] = o.Seed[i%len(o.Seed)]
		}
	}
	out.Write(boxHeader("mdat", 8+len(payload)))
	out.Write(payload)
	return out.Bytes(), nil
}

// Generate creates the synthetic fragment group described by cfg.
func Generate(cfg Config) error {
	if cfg.Tracks < 1 || cfg.Fragments < cfg.Tracks {
		return fmt.Errorf("invalid fixture config: tracks=%d fragments=%d", cfg.Tracks, cfg.Fragments)
	}
	tracks := make([]model.TrackDesc, cfg.Tracks)
	for i := range tracks {
		tracks[i] = TrackDesc(i + 1)
		dir := filepath.Join(cfg.Dir, fmt.Sprintf("track-%04d", i+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	manifest, _ := json.MarshalIndent(tracks, "", "  ")
	if err := os.WriteFile(filepath.Join(cfg.Dir, "tracks.json"), manifest, 0o644); err != nil {
		return err
	}

	type trackState struct {
		mfhd    uint32
		tfdt    uint64
		count   int
		markers bytes.Buffer
	}
	states := make([]*trackState, cfg.Tracks)
	for i := range states {
		states[i] = &trackState{mfhd: 1}
	}
	seedOf := func(i int) []byte {
		s := sha256.Sum256([]byte(fmt.Sprintf("mediapack-frag-%d", i)))
		return s[:]
	}
	writeFrag := func(trackIdx int, name string, data []byte) error {
		p := filepath.Join(cfg.Dir, fmt.Sprintf("track-%04d", trackIdx+1), name)
		return os.WriteFile(p, data, 0o644)
	}
	addMarker := func(st *trackState, trackID int, tfdt uint64) error {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, SequenceNumber: uint16(st.count),
				Timestamp: uint32(tfdt), SSRC: uint32(trackID + 1),
			},
			Payload: []byte{0x4d, 0x50},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(raw)))
		st.markers.Write(lenBuf[:])
		st.markers.Write(raw)
		return nil
	}

	for i := 0; i < cfg.Fragments; i++ {
		t := i % cfg.Tracks
		st := states[t]
		track := tracks[t]
		dur := uint32(3000)
		if track.Type == model.MediaAudio {
			dur = 1024
		}
		keyframe := track.Type == model.MediaAudio || st.count%4 == 0
		o := FragOpts{
			Mfhd: st.mfhd, Tfdt: st.tfdt, HasTfdt: true,
			Keyframe: keyframe, Duration: dur, Seed: seedOf(i),
		}
		// Fault: mfhd gap on track 1's third fragment.
		if has(cfg.Inject, FaultGap) && t == 0 && st.count == 2 {
			o.Mfhd += 2
		}
		// Fault: tfdt backstep on track 2's fourth fragment.
		if has(cfg.Inject, FaultTfdtBackstep) && t == 1%cfg.Tracks && st.count == 3 && st.tfdt > 1000 {
			o.Tfdt = st.tfdt - 1000
		}
		// Fault: missing tfdt on track 3's second fragment.
		if has(cfg.Inject, FaultMissingTfdt) && t == 2%cfg.Tracks && st.count == 1 {
			o.HasTfdt = false
		}
		data, err := BuildFragment(track, o)
		if err != nil {
			return err
		}
		if err := writeFrag(t, fmt.Sprintf("frag-%06d.mp4", i), data); err != nil {
			return err
		}
		if err := addMarker(st, t, o.Tfdt); err != nil {
			return err
		}
		st.mfhd = o.Mfhd + 1
		st.tfdt = o.Tfdt + uint64(dur)
		st.count++
	}
	for t, st := range states {
		p := filepath.Join(cfg.Dir, fmt.Sprintf("track-%04d", t+1), "markers.rtp")
		if err := os.WriteFile(p, st.markers.Bytes(), 0o644); err != nil {
			return err
		}
	}

	// Fault: truncated box — a moof header claiming more bytes than present.
	if has(cfg.Inject, FaultTruncatedBox) {
		data, err := BuildFragment(tracks[0], FragOpts{
			Mfhd: 9001, HasTfdt: true, Keyframe: true, Duration: 3000, Seed: seedOf(-1),
		})
		if err != nil {
			return err
		}
		trunc := data[:len(data)/2]
		binary.BigEndian.PutUint32(trunc[0:4], uint32(len(data))) // moof overruns buffer
		if err := writeFrag(0, "zzz-truncated.mp4", trunc); err != nil {
			return err
		}
	}
	// Fault: duplicate mfhd with conflicting content.
	if has(cfg.Inject, FaultDupMfhd) {
		data, err := BuildFragment(tracks[0], FragOpts{
			Mfhd: 1, HasTfdt: true, Keyframe: true, Duration: 3000, Seed: seedOf(-2),
		})
		if err != nil {
			return err
		}
		if err := writeFrag(0, "zzz-duplicate.mp4", data); err != nil {
			return err
		}
	}
	return nil
}

// PlantPartialIndex simulates a crash that left a half-written first index
// chunk behind: the chunk file is truncated and the checkpoint references
// a checksum that cannot match, so recovery must rebuild the chunk.
func PlantPartialIndex(workDir string) error {
	inputHash, err := batch.HashInputDir(filepath.Join(workDir, "input"))
	if err != nil {
		return err
	}
	ixDir := filepath.Join(workDir, "index")
	if err := os.MkdirAll(ixDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ixDir, "chunk-000000.json"),
		[]byte(`{"index":0,"entries":[{"track":1`), 0o644); err != nil {
		return err
	}
	cp := &batch.Checkpoint{
		InputHash: inputHash,
		Chunks: []batch.ChunkRecord{
			{Index: 0, Count: 8, NextInput: 8, SHA256: "deadbeef", Done: true},
		},
	}
	return batch.SaveCheckpoint(workDir, cp)
}
