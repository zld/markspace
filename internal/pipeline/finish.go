package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"mediarun/internal/audio"
	"mediarun/internal/provenance"
	"mediarun/internal/rtp"
)

// applyLoudness 第二遍：按目标响度施加固定增益，输出最终 PCM 与统计。
func (r *Runner) applyLoudness(a *audioAccum, layout audio.Layout) (audio.Stats, error) {
	st := audio.Stats{Track: a.track.Number, InSamples: a.inSamp,
		OutSamples: a.outSamp, PeakIn: a.peakIn}
	n := a.outSamp * layout.Channels()
	if n == 0 {
		st.LUFSIn, st.LUFSOut = -144, -144
		return st, os.WriteFile(r.path(finalPCM(a.track.Number)), nil, 0o644)
	}
	rms := math.Sqrt(a.sumsq / float64(n))
	st.LUFSIn = -0.691 + 20*math.Log10(rms)
	gain := audio.GainFor(st.LUFSIn, r.Params.LUFS)
	in, err := os.Open(r.path(rawPCM(a.track.Number)))
	if err != nil {
		return st, err
	}
	defer in.Close()
	outTmp, err := os.Create(r.path(finalPCM(a.track.Number) + ".tmp"))
	if err != nil {
		return st, err
	}
	var sumsq, peak float64
	buf := make([]byte, 64*1024)
	for {
		nb, err := in.Read(buf)
		if nb > 0 {
			nb -= nb % 2
			pcm := make([]int16, nb/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
			}
			audio.ApplyGain(pcm, gain)
			for i, s := range pcm {
				v := float64(s) / 32768.0
				sumsq += v * v
				if -v > peak {
					peak = -v
				}
				if v > peak {
					peak = v
				}
				binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
			}
			outTmp.Write(buf[:nb])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, err
		}
	}
	if err := outTmp.Close(); err != nil {
		return st, err
	}
	if err := os.Rename(outTmp.Name(), r.path(finalPCM(a.track.Number))); err != nil {
		return st, err
	}
	st.PeakOut = peak
	st.LUFSOut = -0.691 + 20*math.Log10(math.Sqrt(sumsq/float64(n)))
	return st, nil
}

func rawPCM(track uint64) string   { return "audio1-" + strconv.FormatUint(track, 10) + ".raw" }
func finalPCM(track uint64) string { return "audio-final-" + strconv.FormatUint(track, 10) + ".pcm" }

// stageRTP 恢复本地 RTP 样例流，超窗缺包计入不可恢复。
func (r *Runner) stageRTP() error {
	f, err := os.Open(r.RTPPath)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.Create(r.path("rtp-out.bin"))
	if err != nil {
		return err
	}
	defer out.Close()
	rec := rtp.NewRecoverer(rtp.Config{Window: r.Params.RTPWindow})
	var hdr [2]byte
	for {
		start := time.Now()
		_, err := io.ReadFull(f, hdr[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		frame := make([]byte, binary.BigEndian.Uint16(hdr[:]))
		if _, err := io.ReadFull(f, frame); err != nil {
			return err
		}
		pkt, err := rtp.UnmarshalPacket(frame)
		if err != nil {
			return err
		}
		delivered, err := rec.Push(pkt)
		if err != nil {
			return err
		}
		for _, p := range delivered {
			out.Write(p)
		}
		r.addLatency(float64(time.Since(start).Microseconds()) / 1000)
	}
	stats := rec.Stats()
	return writeJSONAtomic(r.path("rtpstats.json"), &stats)
}

// finalize 计算结果摘要、写清单并核验结果结构。
func (r *Runner) finalize() error {
	var meta metaFile
	if err := readJSON(r.path("meta.json"), &meta); err != nil {
		return err
	}
	var rtpStats rtp.Stats
	if err := readJSON(r.path("rtpstats.json"), &rtpStats); err != nil {
		return err
	}
	h := sha256.New()
	io.WriteString(h, r.Params.Canonical()+"\n")
	io.WriteString(h, r.MediaSHA+"\n"+r.RTPSHA+"\n")
	for _, name := range []string{"blocks.jsonl", "rtp-out.bin"} {
		if err := hashFile(h, r.path(name)); err != nil {
			return err
		}
	}
	for _, st := range meta.Audio {
		if err := hashFile(h, r.path(finalPCM(st.Track))); err != nil {
			return err
		}
	}
	resultSHA := hex.EncodeToString(h.Sum(nil))
	m := &provenance.Manifest{
		SourceSHA256: r.MediaSHA, RTPSourceSHA: r.RTPSHA,
		ParamsSHA256: r.Params.SHA256(), ParamsJSON: r.Params.Canonical(),
		Errors: meta.Errors, AudioStats: meta.Audio, RTPStats: rtpStats,
		Unrecovered: rtpStats.Unrecovered, AVSyncMS: meta.AVSyncMS,
		ResultSHA256: resultSHA,
	}
	if err := provenance.Write(r.path("manifest.json"), m); err != nil {
		return err
	}
	if err := provenance.WriteStructure(r.path("result-structure.mp4"), resultSHA); err != nil {
		return err
	}
	return provenance.VerifyStructure(r.path("result-structure.mp4"), resultSHA)
}

func hashFile(h io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
