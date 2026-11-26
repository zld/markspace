package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"assetpack/internal/clock"
)

// MaxParseWindow is the promised per-input parser window bound.
const MaxParseWindow int64 = 1 << 20

// MP4ManifestBox is the deterministic "apmd" reference descriptor.
type MP4ManifestBox struct {
	APIVersion int           `json:"api_version"`
	Format     string        `json:"format"`
	Duration   clock.Ticks   `json:"duration"`
	Tracks     []MP4TrackBox `json:"tracks"`
	Events     []MP4EventBox `json:"events,omitempty"`
}

type MP4TrackBox struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Codec      ID             `json:"codec"`
	Timebase   clock.Rational `json:"timebase"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	SampleRate int            `json:"sample_rate,omitempty"`
	Channels   int            `json:"channels,omitempty"`
	Samples    []MP4SampleBox `json:"samples"`
}

type MP4SampleBox struct {
	Index   int   `json:"index"`
	PTS     int64 `json:"pts"`
	Dur     int64 `json:"dur"`
	Offset  int64 `json:"offset"`
	Size    int   `json:"size"`
	Key     bool  `json:"key"`
	Preroll []int `json:"preroll,omitempty"`
}

type MP4EventBox struct {
	ID              int    `json:"id"`
	TrackID         string `json:"track_id"`
	Timescale       uint32 `json:"timescale"`
	PresentationPTS uint64 `json:"presentation_pts"`
	Duration        uint32 `json:"duration"`
	SchemeIDURI     string `json:"scheme_id_uri"`
	Value           string `json:"value"`
	Message         []byte `json:"message,omitempty"`
}

type MP4V1 struct{ format string }

func NewMP4V1(format string) *MP4V1 { return &MP4V1{format: format} }

func (a *MP4V1) Descriptor() Descriptor {
	return Descriptor{ID: ID{Name: a.format, Version: APIVersion}, Kind: "container"}
}

func (a *MP4V1) Detect(head []byte) bool {
	return len(head) >= 8 && string(head[4:8]) == "ftyp"
}

func (a *MP4V1) Parse(ctx context.Context, src Source) (*Asset, error) {
	window := src.MaxWindowBytes
	if window == 0 {
		window = MaxParseWindow
	}
	if window > MaxParseWindow {
		return nil, fmt.Errorf("%w: parse window %d exceeds %d", ErrBadInput, window, MaxParseWindow)
	}
	ra, size, err := src.Reader()
	if err != nil {
		return nil, err
	}
	if c, ok := ra.(io.Closer); ok {
		defer c.Close()
	}
	rs := io.NewSectionReader(ra, 0, size)
	sum, err := hashReaderAt(ra, size)
	if err != nil {
		return nil, clock.Wrap("mp4", "hash", 0, err)
	}

	var manifest MP4ManifestBox
	var nativeEvents []Event
	sawMDAT, sawFragment := false, false
	_, err = mp4.ReadBoxStructure(rs, func(h *mp4.ReadHandle) (interface{}, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bi := h.BoxInfo
		if bi.Offset+bi.Size > uint64(size) {
			return nil, clock.Wrap("mp4", "box-bounds", int64(bi.Offset),
				fmt.Errorf("%w: box %s declared end %d exceeds file size %d (truncated mdat?)",
					ErrBadInput, bi.Type, bi.Offset+bi.Size, size))
		}
		switch bi.Type.String() {
		case "apmd":
			payloadSize := int64(bi.Size - bi.HeaderSize)
			if payloadSize > window {
				return nil, fmt.Errorf("%w: apmd payload %d exceeds window %d", ErrBadInput, payloadSize, window)
			}
			payload := make([]byte, payloadSize)
			if _, err := io.ReadFull(rs, payload); err != nil {
				return nil, clock.Wrap("mp4", "read-apmd", int64(bi.Offset), err)
			}
			if err := json.Unmarshal(payload, &manifest); err != nil {
				return nil, clock.Wrap("mp4", "decode-apmd", int64(bi.Offset), err)
			}
		case "emsg":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, clock.Wrap("event", "read-emsg", int64(bi.Offset), err)
			}
			e := box.(*mp4.Emsg)
			ev, err := emsgToEvent(e)
			if err != nil {
				return nil, err
			}
			nativeEvents = append(nativeEvents, ev)
		case "mdat":
			sawMDAT = true
		case "moof":
			sawFragment = true
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if manifest.APIVersion != APIVersion || manifest.Format != a.format {
		return nil, fmt.Errorf("%w: expected %s/v1 manifest, got %q/v%d",
			ErrUnknownVersion, a.format, manifest.Format, manifest.APIVersion)
	}
	if !sawMDAT {
		return nil, fmt.Errorf("%w: %s requires mdat", ErrBadInput, a.format)
	}
	if a.format == FormatFMP4 && !sawFragment {
		return nil, fmt.Errorf("%w: fmp4 requires moof", ErrBadInput)
	}

	asset := &Asset{
		Descriptor: a.Descriptor(), SourceName: src.Name, SourceSHA: sum,
		Duration: manifest.Duration, Events: nativeEvents,
	}
	for _, td := range manifest.Tracks {
		tr := &Track{
			ID: td.ID, Kind: td.Kind, Codec: td.Codec, Timebase: td.Timebase,
			Width: td.Width, Height: td.Height, SampleRate: td.SampleRate, Channels: td.Channels,
		}
		for _, sd := range td.Samples {
			pm, err := clock.Map(td.ID, "mp4-pts", sd.Offset, sd.PTS, td.Timebase)
			if err != nil {
				return nil, err
			}
			dm, err := clock.Map(td.ID, "mp4-duration", sd.Offset, sd.Dur, td.Timebase)
			if err != nil {
				return nil, err
			}
			if !pm.Aligned() || !dm.Aligned() {
				return nil, clock.Wrap(td.ID, "mp4-align", sd.Offset,
					fmt.Errorf("PTS/duration not tick aligned: pts rem=%d/%d", pm.RemNum, pm.RemDen))
			}
			if int64(sd.Offset)+int64(sd.Size) > size {
				return nil, clock.Wrap(td.ID, "sample-bounds", sd.Offset,
					fmt.Errorf("%w: sample %d ends beyond file", ErrBadInput, sd.Index))
			}
			tr.Samples = append(tr.Samples, Sample{
				Index: sd.Index, TrackID: td.ID, PTS: pm.Ticks, Duration: dm.Ticks,
				Offset: sd.Offset, Size: sd.Size, Key: sd.Key, Preroll: append([]int(nil), sd.Preroll...),
			})
		}
		if len(tr.Samples) == 0 {
			return nil, fmt.Errorf("%w: track %s has no samples", ErrBadInput, td.ID)
		}
		tr.Duration = tr.Samples[len(tr.Samples)-1].PTS + tr.Samples[len(tr.Samples)-1].Duration
		asset.Tracks = append(asset.Tracks, tr)
	}
	for _, ed := range manifest.Events {
		m, err := clock.Map(ed.TrackID, "emsg-pts", 0, int64(ed.PresentationPTS), clock.Timescale(int64(ed.Timescale)))
		if err != nil {
			return nil, err
		}
		dm, _ := clock.Map(ed.TrackID, "emsg-duration", 0, int64(ed.Duration), clock.Timescale(int64(ed.Timescale)))
		asset.Events = append(asset.Events, Event{
			ID: ed.ID, TrackID: ed.TrackID, PTS: m.Ticks, Duration: dm.Ticks,
			SchemeIDURI: ed.SchemeIDURI, Value: ed.Value, SourceTimescale: ed.Timescale,
			SourcePTS: ed.PresentationPTS, MessageData: ed.Message,
		})
	}
	return asset, nil
}

func emsgToEvent(e *mp4.Emsg) (Event, error) {
	ts := e.Timescale
	pts := e.PresentationTime
	if e.GetVersion() == 0 {
		pts = uint64(e.PresentationTimeDelta)
	}
	m, err := clock.Map("emsg", "emsg", int64(e.Id), int64(pts), clock.Timescale(int64(ts)))
	if err != nil {
		return Event{}, err
	}
	dm, err := clock.Map("emsg", "emsg-duration", int64(e.Id), int64(e.EventDuration), clock.Timescale(int64(ts)))
	if err != nil {
		return Event{}, err
	}
	return Event{
		ID: int(e.Id), PTS: m.Ticks, Duration: dm.Ticks, SchemeIDURI: e.SchemeIdUri,
		Value: e.Value, SourceTimescale: ts, SourcePTS: pts, MessageData: e.MessageData,
	}, nil
}

func hashReaderAt(ra io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(ra, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
