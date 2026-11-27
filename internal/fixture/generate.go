package fixture

// DemoSet is the complete, self-contained material for one demo run. Every
// byte is generated here; no external files are referenced.
type DemoSet struct {
	// Normal inputs.
	MXFPath string
	MP4Path string
	MXFRaw  []byte
	MP4Raw  []byte
	// Corrupted MP4: a box whose declared size runs past EOF.
	CorruptPath string
	CorruptRaw  []byte
	CorruptAt   int64
	// Explicit source->target mapping rules for the normal inputs.
	MXFRules []Rule
	MP4Rules []Rule
}

// Rule is the fixture-level mapping instruction, kept free of pipeline
// imports so the package stays a leaf.
type Rule struct {
	SourcePath  string
	SourceTrack uint32
	TargetTrack uint32
	Role        string
}

// RecoveryScenario drives the late-event/clock-wrap demonstration.
type RecoveryScenario struct {
	Events []TimedEvent
	Late   TimedEvent
}

// TimedEvent is the fixture-neutral event shape; the pipeline translates it
// into a timeline.Event. RTPRaw near the 32-bit ceiling forces a wrap.
type TimedEvent struct {
	Seq          uint64
	Source       string
	TrackID      uint32
	SampleIndex  uint32
	OriginalTick int64
	Duration     int64
	RTPRaw       uint32
	HasRTP       bool
	Late         bool
}

// DemoMXFSpecs builds the normal MXF tracks. Track 1 carries an RTP timestamp
// that crosses the 32-bit wrap within the sample count.
func DemoMXFSpecs() []MXFTrackSpec {
	return []MXFTrackSpec{
		{
			TrackID: 1, Timescale: 25, Codec: "vraw", Handler: "vide",
			Rotation: 90, CropL: 4, CropT: 2, CropR: 4, CropB: 2,
			ColorPrim: 9, ColorTRC: 16, ColorMx: 9, FullRange: true,
			// Start near the ceiling so a few 3600-tick steps wrap the raw u32.
			RTPBase: 0xffffff80, RTPStep: 0x200, Count: 10, Size: 48,
		},
		{
			TrackID: 2, Timescale: 48000, Codec: "pcm ", Handler: "soun",
			ColorPrim: 1, ColorTRC: 1, ColorMx: 1,
			RTPBase: 0x100, RTPStep: 1920, Count: 8, Size: 24,
		},
	}
}

// DemoMP4Specs builds the normal MP4 tracks sharing the same logical track
// numbers, so metadata adjudication across MXF and MP4 is observable.
func DemoMP4Specs() []MP4TrackSpec {
	return []MP4TrackSpec{
		{
			TrackID: 1, Timescale: 24000, Codec: "avc1", Handler: "vide",
			Rotation: 180, CropL: 8, CropT: 4, CropR: 8, CropB: 4,
			ColorPrim: 1, ColorTRC: 1, ColorMx: 1, FullRange: false,
			Count: 10, Size: 48,
		},
		{
			TrackID: 2, Timescale: 48000, Codec: "mp4a", Handler: "soun",
			ColorPrim: 1, ColorTRC: 1, ColorMx: 1,
			Count: 8, Size: 24,
		},
	}
}

// DemoRules returns the explicit source-to-target mapping used by the demo.
func DemoRules(mxfPath, mp4Path string) ([]Rule, []Rule) {
	mxf := []Rule{
		{SourcePath: mxfPath, SourceTrack: 1, TargetTrack: 11, Role: "vide"},
		{SourcePath: mxfPath, SourceTrack: 2, TargetTrack: 12, Role: "soun"},
	}
	mp4 := []Rule{
		{SourcePath: mp4Path, SourceTrack: 1, TargetTrack: 21, Role: "vide"},
		{SourcePath: mp4Path, SourceTrack: 2, TargetTrack: 22, Role: "soun"},
	}
	return mxf, mp4
}

// BuildDemoSet generates the normal and corrupted byte images. The corruption
// truncates the final box so its declared size extends past EOF; CorruptAt is
// the offset of the first illegal box header.
func BuildDemoSet(mxfPath, mp4Path, corruptPath string) (*DemoSet, error) {
	mxfRaw, _, err := MXF(DemoMXFSpecs())
	if err != nil {
		return nil, err
	}
	mp4Raw, _, err := MP4(DemoMP4Specs())
	if err != nil {
		return nil, err
	}
	// Cut well inside the trailing mdat payload; the mdat box header then
	// declares more bytes than the file provides.
	cut := len(mp4Raw) - 40
	corrupt := append([]byte(nil), mp4Raw[:cut]...)
	// First illegal offset is the start of the now-truncated final box region.
	// The mdat header begins before the cut point; its declared size overruns.
	mdatAt := findMdatHeader(mp4Raw)
	corruptAt := int64(mdatAt)
	if mdatAt < 0 {
		corruptAt = int64(cut)
	}
	mxfRules, mp4Rules := DemoRules(mxfPath, mp4Path)
	return &DemoSet{
		MXFPath: mxfPath, MP4Path: mp4Path,
		MXFRaw: mxfRaw, MP4Raw: mp4Raw,
		CorruptPath: corruptPath, CorruptRaw: corrupt, CorruptAt: corruptAt,
		MXFRules: mxfRules, MP4Rules: mp4Rules,
	}, nil
}

// findMdatHeader returns the file offset of the mdat box header, or -1.
func findMdatHeader(raw []byte) int {
	for i := 0; i+8 <= len(raw); i++ {
		if string(raw[i+4:i+8]) == "mdat" {
			return i
		}
	}
	return -1
}

// BuildRecoveryScenario emits an event stream with an RTP wrap on track 1 and
// one explicitly late event whose edge predates the watermark.
func BuildRecoveryScenario() RecoveryScenario {
	evs := []TimedEvent{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0,
			OriginalTick: 1000, Duration: 100, RTPRaw: 0xffffff80, HasRTP: true},
		{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1,
			OriginalTick: 1100, Duration: 100, RTPRaw: 0xffffff90, HasRTP: true},
		{Seq: 3, Source: "a", TrackID: 1, SampleIndex: 2,
			OriginalTick: 1200, Duration: 100, RTPRaw: 0xffffffa0, HasRTP: true},
		{Seq: 4, Source: "a", TrackID: 1, SampleIndex: 3,
			OriginalTick: 1300, Duration: 100, RTPRaw: 0x00000010, HasRTP: true},
		{Seq: 5, Source: "a", TrackID: 1, SampleIndex: 4,
			OriginalTick: 1400, Duration: 100, RTPRaw: 0x00000020, HasRTP: true},
		{Seq: 6, Source: "a", TrackID: 1, SampleIndex: 5,
			OriginalTick: 1500, Duration: 100, RTPRaw: 0x00000030, HasRTP: true},
	}
	// Late event: timestamp 1250 sits between already-stable edges.
	late := TimedEvent{Seq: 7, Source: "a", TrackID: 1, SampleIndex: 6,
		OriginalTick: 1250, Duration: 100, RTPRaw: 0xffffffa8, HasRTP: true, Late: true}
	return RecoveryScenario{Events: evs, Late: late}
}
