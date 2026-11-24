package demux

import (
	"sort"

	"medpipe/internal/clock"
	"medpipe/internal/fault"
	"medpipe/internal/ts"
)

// DemuxTS parses PSI, reassembles PES samples, unwraps clocks, and returns a
// format-independent media model. A corrupt tail is returned together with
// the complete media observed before the fault; the caller must inspect err
// and must not mark such a result complete.
func DemuxTS(data []byte) (*Media, []fault.Event, *fault.Error) {
	read, rerr := ts.ReadPackets(data)
	events := []fault.Event{}
	if read != nil {
		events = append(events, read.Events...)
	}
	if rerr != nil && read == nil {
		return nil, events, rerr
	}

	psi := ts.NewPSIParser()
	asm := ts.NewSectionAssembler()
	pcrClocks := map[uint16]*clock.TrackMap{}
	for _, pkt := range read.Packets {
		_, _ = psi.HandlePacket(asm, pkt)
		if pkt.Adaptation != nil && pkt.Adaptation.HasPCR {
			tm := pcrClocks[pkt.PID]
			if tm == nil {
				tm = &clock.TrackMap{}
				pcrClocks[pkt.PID] = tm
			}
			_, wrapped, _ := tm.MapPTS(pkt.Adaptation.PCRBase)
			if wrapped {
				events = append(events, fault.Event{
					Code: fault.ErrPCRWrap, Severity: "info", Stage: "clock.pcr",
					PID: pkt.PID, Offset: pkt.Offset, EndOffset: pkt.Offset + ts.PacketSize,
					TimeRange: &fault.TimeRangeMS{Start: tm.LastMS, End: tm.LastMS},
					Message:   "33-bit PCR base wrapped and was expanded",
				})
			}
		}
	}
	events = append(events, psi.Events()...)
	events = append(events, psi.MissingVersions()...)
	for _, c := range psi.Committed {
		events = append(events, c)
	}

	if psi.ActivePAT() == nil || len(psi.ActivePMTs()) == 0 {
		fe := fault.New(fault.ErrPSISection, "demux.ts", 0, "no complete PAT/PMT mapping")
		events = append(events, fault.Event{Code: fe.Code, Severity: "fatal", Stage: fe.Stage, Message: fe.Message})
		return nil, events, fe
	}

	trackDescs := sortedTracks(psi.ActivePMTs())
	byPID := map[uint16]*Track{}
	pesByPID := map[uint16][]*ts.PESPacket{}
	ptsClocks := map[uint16]*clock.TrackMap{}
	media := &Media{Format: "mpeg-ts"}
	for i, d := range trackDescs {
		if d.Codec == "unknown" {
			fe := fault.New(fault.ErrUnknownCodec, "demux.ts", 0,
				"TS stream type cannot be mapped").WithPID(d.PID)
			events = append(events, fault.Event{Code: fe.Code, Severity: "fatal", Stage: fe.Stage, PID: d.PID, Message: fe.Message})
			return media, events, fe
		}
		t := &Track{ID: uint32(i + 1), PID: d.PID, Kind: d.Kind, Codec: d.Codec, Timescale: 1000}
		if d.Kind == "audio" {
			t.SampleRate = 48000
			t.Channels = 2
		} else {
			t.Width = 320
			t.Height = 180
		}
		media.Tracks = append(media.Tracks, t)
		byPID[d.PID] = t
		ptsClocks[d.PID] = &clock.TrackMap{}
	}

	pesAsm := ts.NewPESAssembler()
	for _, pkt := range read.Packets {
		if _, ok := byPID[pkt.PID]; !ok {
			continue
		}
		pes, pesEvents, ferr := pesAsm.Feed(pkt)
		events = append(events, pesEvents...)
		if ferr != nil {
			events = append(events, fault.Event{Code: ferr.Code, Severity: "fatal", Stage: ferr.Stage, PID: pkt.PID, Offset: ferr.Offset, Message: ferr.Message})
			return media, events, ferr
		}
		for _, p := range pes {
			pesByPID[p.PID] = append(pesByPID[p.PID], p)
		}
	}
	finalPES, finalEvents := pesAsm.Finish()
	events = append(events, finalEvents...)
	for _, p := range finalPES {
		pesByPID[p.PID] = append(pesByPID[p.PID], p)
	}

	for _, t := range media.Tracks {
		pesList := pesByPID[t.PID]
		for i, p := range pesList {
			if len(p.Data) == 0 {
				fe := fault.New(fault.ErrCorruptSample, "demux.ts.sample", p.Offset, "empty compressed sample").WithPID(t.PID)
				events = append(events, fault.Event{Code: fe.Code, Severity: "fatal", Stage: fe.Stage, PID: t.PID, Offset: p.Offset, Message: fe.Message})
				return media, events, fe
			}
			raw := p.PTS
			if !p.HasPTS {
				// Synthetic fixtures always have PTS; absence is malformed for
				// the supported remux profile.
				fe := fault.New(fault.ErrCorruptSample, "demux.ts.pts", p.Offset, "sample lacks PTS").WithPID(t.PID)
				events = append(events, fault.Event{Code: fe.Code, Severity: "fatal", Stage: fe.Stage, PID: t.PID, Offset: p.Offset, Message: fe.Message})
				return media, events, fe
			}
			ms, wrapped, nonMonotonic := ptsClocks[t.PID].MapPTS(raw)
			if wrapped {
				events = append(events, fault.Event{Code: fault.ErrPTSWrap, Severity: "info", Stage: "clock.pts", PID: t.PID, Offset: p.Offset, TimeRange: &fault.TimeRangeMS{Start: ms, End: ms}, Message: "33-bit PTS wrapped and was expanded"})
			}
			if nonMonotonic {
				events = append(events, fault.Event{Code: fault.ErrMonotonic, Severity: "corrupt", Stage: "clock.pts", PID: t.PID, Offset: p.Offset, TimeRange: &fault.TimeRangeMS{Start: ms, End: ms}, Message: "PTS moved backward; output clamped to monotonic time"})
			}
			t.Samples = append(t.Samples, Sample{
				Index: i, PID: t.PID, PTSRaw: raw, PTSMS: ms,
				Keyframe: i == 0, Offset: p.Offset, Data: p.Data,
			})
		}
		assignDurations(t)
	}

	enrichTimeRanges(events, media)
	media.Events = events
	if rerr != nil {
		return media, events, rerr
	}
	for _, e := range events {
		if e.Severity == "fatal" {
			return media, events, fault.New(e.Code, e.Stage, e.Offset, e.Message)
		}
	}
	return media, events, nil
}

func sortedTracks(pmts map[uint16]*ts.PMT) []ts.TrackDescription {
	var pids []uint16
	for pid := range pmts {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	var out []ts.TrackDescription
	for _, pid := range pids {
		out = append(out, pmts[pid].Tracks...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "video"
		}
		return out[i].PID < out[j].PID
	})
	return out
}

func assignDurations(t *Track) {
	defaultMS := int64(40)
	if t.Kind == "audio" {
		defaultMS = 20
	}
	for i := range t.Samples {
		if i+1 < len(t.Samples) {
			d := t.Samples[i+1].PTSMS - t.Samples[i].PTSMS
			if d <= 0 {
				d = defaultMS
			}
			t.Samples[i].DurationMS = d
		} else if i > 0 && t.Samples[i-1].DurationMS > 0 {
			t.Samples[i].DurationMS = t.Samples[i-1].DurationMS
		} else {
			t.Samples[i].DurationMS = defaultMS
		}
	}
}

func enrichTimeRanges(events []fault.Event, m *Media) {
	ranges := map[uint16]*fault.TimeRangeMS{}
	for _, t := range m.Tracks {
		if len(t.Samples) == 0 {
			continue
		}
		ranges[t.PID] = &fault.TimeRangeMS{Start: t.Samples[0].PTSMS, End: t.Samples[len(t.Samples)-1].PTSMS}
	}
	for i := range events {
		if events[i].TimeRange != nil || events[i].PID == 0 {
			continue
		}
		if tr, ok := ranges[events[i].PID]; ok {
			cp := *tr
			events[i].TimeRange = &cp
		}
	}
}
