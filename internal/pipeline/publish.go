package pipeline

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/container"
	"mediacheck/internal/fault"
	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
	"mediacheck/internal/migrate"
	"mediacheck/internal/rollback"
	"mediacheck/internal/sample"
)

// trackSpec derives a container track from one asset: one sample per
// picture unit, keyframe sample numbers from the index.
func trackSpec(id uint32, a *sample.Asset) container.TrackSpec {
	spec := container.TrackSpec{
		ID: id, Kind: a.Kind,
		Timescale: uint32(clock.TBMedia), SampleDelta: sample.FrameTicks,
	}
	pic := uint32(0)
	for _, u := range a.Units {
		if !sample.IsPicture(u) {
			continue
		}
		pic++
		spec.Samples = append(spec.Samples, a.Stream[u.StartCode:u.Offset+int64(u.Size)])
		if u.Keyframe {
			spec.KeySamples = append(spec.KeySamples, pic)
		}
	}
	return spec
}

// boundarySamples generates edge-case inputs and verifies the parser
// and index report the expected stable codes.
func boundarySamples(out io.Writer) error {
	if units, err := codec.Units(nil, codec.H264); err != nil || len(units) != 0 {
		return fault.New(fault.ErrIntegrity, fault.NoOffset, "boundary sample empty misbehaved")
	}
	fmt.Fprintln(out, "boundary sample=empty units=0 ok")

	a, err := sample.Generate(codec.H264)
	if err != nil {
		return err
	}
	cut := int(a.Units[1].StartCode) + 4 // start code with no header after it
	if _, err := codec.Units(a.Stream[:cut], codec.H264); !fault.HasCode(err, fault.ErrTruncatedUnit) {
		return fault.New(fault.ErrIntegrity, int64(cut), "boundary sample truncated: %v", err)
	}
	fmt.Fprintf(out, "boundary sample=truncated offset=%d code=%s ok\n", cut, fault.ErrTruncatedUnit)

	bad := sample.CorruptAt(a.Stream, sample.HeaderOffset)
	if _, err := codec.Units(bad, codec.H264); !fault.HasCode(err, fault.ErrBadHeader) {
		return fault.New(fault.ErrIntegrity, sample.HeaderOffset, "boundary sample bad-header: %v", err)
	}
	fmt.Fprintf(out, "boundary sample=bad-header offset=%d code=%s ok\n",
		sample.HeaderOffset, fault.ErrBadHeader)

	over := index.Index{Offsets: []int64{int64(len(a.Stream))}}
	if err := over.Validate(a.Units, int64(len(a.Stream))); !fault.HasCode(err, fault.ErrIndexOutOfRange) {
		return fault.New(fault.ErrIntegrity, fault.NoOffset, "boundary sample index: %v", err)
	}
	fmt.Fprintf(out, "boundary sample=index-range offset=%d code=%s ok\n",
		len(a.Stream), fault.ErrIndexOutOfRange)
	return nil
}

// publish writes release r1 (legacy v0 manifest), migrates the
// manifest to v1 in validated stages, then publishes release r2 and
// atomically switches the active pointer.
func publish(opts Options, out io.Writer, segs []manifest.SegmentEntry,
	frames []manifest.FrameEntry, peak, alignMax float64) error {
	store := &rollback.Store{Root: opts.Workdir}
	codecs := make([]string, len(opts.Codecs))
	for i, k := range opts.Codecs {
		codecs[i] = k.String()
	}
	v0, err := manifest.MarshalV0(&manifest.V0{Codecs: codecs, Segments: segs})
	if err != nil {
		return err
	}
	r1, err := store.Begin("r1")
	if err != nil {
		return err
	}
	if err := r1.Write("manifest.json", v0); err != nil {
		return err
	}
	if err := r1.Finalize(); err != nil {
		return err
	}
	if err := store.Switch("r1"); err != nil {
		return err
	}
	fmt.Fprintln(out, "publish release=r1 manifest=v0 active=r1")

	v1 := &manifest.Manifest{
		Codecs: codecs, Timebase: uint32(clock.TBMedia),
		Segments: segs, Frames: frames,
		Quality: manifest.Quality{LoudnessPeakDBFS: peak, MaxAlignError: alignMax},
	}
	staging := filepath.Join(opts.Workdir, "staging")
	steps := []migrate.Step{
		{Name: "migrate-1",
			Apply: func(dir string) error {
				m, err := manifest.Parse(v0)
				if err != nil {
					return err
				}
				data, err := manifest.MarshalV0(&manifest.V0{Codecs: m.Codecs, Segments: m.Segments})
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "normalized.json"), data, 0o644)
			},
			Check: func(dir string) error {
				data, err := os.ReadFile(filepath.Join(dir, "normalized.json"))
				if err != nil {
					return err
				}
				_, err = manifest.Parse(data)
				return err
			}},
		{Name: "migrate-2",
			Apply: func(dir string) error {
				data, err := manifest.Marshal(v1)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644)
			},
			Check: func(dir string) error {
				data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err != nil {
					return err
				}
				m, err := manifest.Parse(data)
				if err != nil {
					return err
				}
				if m.Version != manifest.CurrentVersion {
					return fmt.Errorf("staged manifest version %d", m.Version)
				}
				return nil
			}},
	}
	if err := migrate.Run(steps, staging, opts.InterruptAfter); err != nil {
		if fault.HasCode(err, fault.ErrInterrupted) {
			fmt.Fprintf(out, "interrupted: %v\n", err)
			if opts.Recover {
				return recoverStore(store, out)
			}
		}
		return err
	}

	final, err := os.ReadFile(filepath.Join(staging, "migrate-2", "manifest.json"))
	if err != nil {
		return err
	}
	r2, err := store.Begin("r2")
	if err != nil {
		return err
	}
	if err := r2.Write("manifest.json", final); err != nil {
		return err
	}
	if err := r2.Finalize(); err != nil {
		return err
	}
	if err := store.Switch("r2"); err != nil {
		return err
	}
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	fmt.Fprintln(out, "publish release=r2 manifest=v1 active=r2")
	if opts.Recover {
		return recoverStore(store, out)
	}
	return nil
}

// recoverStore reports the post-interruption state: exactly one
// active version, fully intact (complete old or complete new).
func recoverStore(store *rollback.Store, out io.Writer) error {
	rep, err := store.Recover()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "recover active=%s intact=%v removed=%d active_versions=1\n",
		rep.Active, rep.ActiveIntact, len(rep.Removed))
	return nil
}
