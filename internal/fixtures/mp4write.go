// Package fixtures builds every sample used by tests and by the
// `analyze --fixture generated` demo. Nothing here reads external files.
package fixtures

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// MP4Sample describes one video sample the writer will emit.
type MP4Sample struct {
	Size   uint32
	Delta  uint32 // in track timescale ticks
	IsSync bool
}

// MP4Spec is a compact, fully specified non-fragmented MP4: one video track
// whose samples live in one chunk. Building it by hand keeps fixtures
// deterministic and independent of any encoder.
type MP4Spec struct {
	Timescale uint32
	Samples   []MP4Sample
	Width     uint16
	Height    uint16
}

type buf struct{ b []byte }

func (w *buf) u8(v uint8)   { w.b = append(w.b, v) }
func (w *buf) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *buf) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *buf) u64(v uint64) { w.b = binary.BigEndian.AppendUint64(w.b, v) }
func (w *buf) zeros(n int) {
	for i := 0; i < n; i++ {
		w.b = append(w.b, 0)
	}
}
func (w *buf) str4(s string) {
	if len(s) != 4 {
		panic("box type must be 4 bytes: " + s)
	}
	w.b = append(w.b, s...)
}

// box writes a complete box whose payload is produced by fn.
func box(typ string, fn func(p *buf)) []byte {
	p := &buf{}
	fn(p)
	out := &buf{}
	out.u32(uint32(len(p.b) + 8))
	out.str4(typ)
	out.b = append(out.b, p.b...)
	return out.b
}

func fullbox(typ string, version uint8, flags uint32, fn func(p *buf)) []byte {
	return box(typ, func(p *buf) {
		p.u8(version)
		p.u8(uint8(flags >> 16))
		p.u8(uint8(flags >> 8))
		p.u8(uint8(flags))
		if fn != nil {
			fn(p)
		}
	})
}

// BuildMP4 renders a complete playable-looking MP4 (ftyp/moov/mdat). Sample
// payloads are zeros of the declared sizes; only structural integrity and
// metadata matter to the analyzer.
func BuildMP4(spec MP4Spec) []byte {
	if spec.Timescale == 0 {
		spec.Timescale = 90000
	}
	if spec.Width == 0 {
		spec.Width = 1920
	}
	if spec.Height == 0 {
		spec.Height = 1080
	}
	var totalDur uint64
	stts := groupDeltas(spec.Samples)
	for _, s := range spec.Samples {
		totalDur += uint64(s.Delta)
	}
	var mdatPayload []byte
	var chunkOffset uint32
	// Layout: ftyp | moov | mdat, so mdat offset equals len(ftyp)+len(moov)+8.
	ftyp := box("ftyp", func(p *buf) {
		p.str4("isom")
		p.u32(0x200)
		p.str4("isom")
		p.str4("iso2")
		p.str4("avc1")
		p.str4("mp41")
	})
	mdat := func(off int) []byte {
		payload := make([]byte, 0, totalSize(spec.Samples))
		for _, s := range spec.Samples {
			payload = append(payload, make([]byte, s.Size)...)
		}
		mdatPayload = payload
		chunkOffset = uint32(off)
		return box("mdat", func(p *buf) { p.b = append(p.b, payload...) })
	}
	// Iterate: moov size depends on stts/stss but not on chunk offset value.
	moov := buildMoov(spec, stts, totalDur, 0)
	off := len(ftyp) + len(moov) + 8
	_ = mdat(off)
	moov = buildMoov(spec, stts, totalDur, chunkOffset)
	out := make([]byte, 0, len(ftyp)+len(moov)+len(mdatPayload)+8)
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, box("mdat", func(p *buf) { p.b = append(p.b, mdatPayload...) })...)
	return out
}

func totalSize(samples []MP4Sample) int {
	n := 0
	for _, s := range samples {
		n += int(s.Size)
	}
	return n
}

type deltaRun struct{ count, delta uint32 }

func groupDeltas(samples []MP4Sample) []deltaRun {
	var runs []deltaRun
	for _, s := range samples {
		if n := len(runs); n > 0 && runs[n-1].delta == s.Delta {
			runs[n-1].count++
			continue
		}
		runs = append(runs, deltaRun{count: 1, delta: s.Delta})
	}
	return runs
}

func buildMoov(spec MP4Spec, runs []deltaRun, totalDur uint64, chunkOffset uint32) []byte {
	return box("moov", func(p *buf) {
		p.b = append(p.b, fullbox("mvhd", 0, 0, func(p *buf) {
			p.u32(0) // creation
			p.u32(0) // modification
			p.u32(spec.Timescale)
			p.u32(uint32(totalDur))
			p.u32(0x00010000) // rate 1.0
			p.u16(0x0100)     // volume
			p.zeros(10)
			for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
				p.u32(v)
			}
			p.zeros(24)
			p.u32(2) // next track id
		})...)
		p.b = append(p.b, box("trak", func(p *buf) {
			p.b = append(p.b, fullbox("tkhd", 0, 0x000003, func(p *buf) {
				p.u32(0)
				p.u32(0)
				p.u32(1) // track id
				p.u32(0)
				p.u32(uint32(totalDur))
				p.zeros(8)
				p.u16(0) // layer
				p.u16(0) // alternate group
				p.u16(0) // volume
				p.u16(0)
				for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
					p.u32(v)
				}
				p.u32(uint32(spec.Width) << 16)
				p.u32(uint32(spec.Height) << 16)
			})...)
			p.b = append(p.b, box("mdia", func(p *buf) {
				p.b = append(p.b, fullbox("mdhd", 0, 0, func(p *buf) {
					p.u32(0)
					p.u32(0)
					p.u32(spec.Timescale)
					p.u32(uint32(totalDur))
					p.u16(0x55c4) // 'und'
					p.u16(0)
				})...)
				p.b = append(p.b, fullbox("hdlr", 0, 0, func(p *buf) {
					p.u32(0)
					p.str4("vide")
					p.zeros(12)
					p.b = append(p.b, "Archive Video\x00"...)
				})...)
				p.b = append(p.b, box("minf", func(p *buf) {
					p.b = append(p.b, fullbox("vmhd", 0, 0x000001, func(p *buf) {
						p.u16(0)
						p.zeros(6)
					})...)
					p.b = append(p.b, box("dinf", func(p *buf) {
						p.b = append(p.b, fullbox("dref", 0, 0, func(p *buf) {
							p.u32(1)
							p.b = append(p.b, fullbox("url ", 0, 0x000001, nil)...)
						})...)
					})...)
					p.b = append(p.b, box("stbl", func(p *buf) {
						p.b = append(p.b, buildStsd(spec)...)
						p.b = append(p.b, fullbox("stts", 0, 0, func(p *buf) {
							p.u32(uint32(len(runs)))
							for _, r := range runs {
								p.u32(r.count)
								p.u32(r.delta)
							}
						})...)
						var sync []uint32
						for i, s := range spec.Samples {
							if s.IsSync {
								sync = append(sync, uint32(i+1))
							}
						}
						if len(sync) != len(spec.Samples) {
							p.b = append(p.b, fullbox("stss", 0, 0, func(p *buf) {
								p.u32(uint32(len(sync)))
								for _, v := range sync {
									p.u32(v)
								}
							})...)
						}
						p.b = append(p.b, fullbox("stsc", 0, 0, func(p *buf) {
							p.u32(1)
							p.u32(1)
							p.u32(uint32(len(spec.Samples)))
							p.u32(1)
						})...)
						p.b = append(p.b, fullbox("stsz", 0, 0, func(p *buf) {
							p.u32(0)
							p.u32(uint32(len(spec.Samples)))
							for _, s := range spec.Samples {
								p.u32(s.Size)
							}
						})...)
						p.b = append(p.b, fullbox("stco", 0, 0, func(p *buf) {
							p.u32(1)
							p.u32(chunkOffset)
						})...)
					})...)
				})...)
			})...)
		})...)
	})
}

func buildStsd(spec MP4Spec) []byte {
	return fullbox("stsd", 0, 0, func(p *buf) {
		p.u32(1)
		p.b = append(p.b, box("avc1", func(p *buf) {
			p.zeros(6)
			p.u16(1) // data reference index
			p.u16(0)
			p.u16(0)
			p.zeros(12)
			p.u16(spec.Width)
			p.u16(spec.Height)
			p.u32(0x00480000)
			p.u32(0x00480000)
			p.u32(0)
			p.u16(1)
			p.zeros(32)
			p.u16(0x0018)
			p.u16(0xffff)
			// avcC with a minimal but well-formed SPS/PPS-free payload.
			p.b = append(p.b, box("avcC", func(p *buf) {
				p.u8(1)    // configurationVersion
				p.u8(0x64) // AVCProfileIndication (high)
				p.u8(0)    // profile_compatibility
				p.u8(0x1f) // AVCLevelIndication
				p.u8(0xff) // lengthSizeMinusOne = 3
				p.u8(0xe1) // 1 SPS
				p.u16(4)
				p.b = append(p.b, 0x67, 0x64, 0x00, 0x1f)
				p.u8(1) // 1 PPS
				p.u16(2)
				p.b = append(p.b, 0x68, 0xee)
			})...)
		})...)
	})
}

// WriteFile writes data atomically via a temporary file in the same
// directory, so a crash never leaves a half-written fixture.
func WriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(dirOf(path), ".fixture-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// CopyTo writes data through an io.Writer, used by tests.
func CopyTo(w io.Writer, data []byte) error {
	_, err := w.Write(data)
	return err
}

// MustBuild is a test helper wrapper with a panic on impossible errors.
func MustBuild(spec MP4Spec) []byte {
	b := BuildMP4(spec)
	if len(b) == 0 {
		panic(fmt.Sprintf("empty mp4 for %d samples", len(spec.Samples)))
	}
	return b
}
