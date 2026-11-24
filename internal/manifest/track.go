package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"mediasync/internal/model"
)

func parseTrack(raw json.RawMessage, file string, index int) (*Track, error) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: file, TrackIndex: index}, "track is not an object").Wrap(err)
	}
	var errs model.ErrorList
	loc := func(field string) model.Loc {
		return model.Loc{File: file, TrackIndex: index, Field: field}
	}
	t := &Track{index: index}

	getInt := func(field string, dst *int64, required bool) {
		v, ok := fields[field]
		if !ok {
			if required {
				errs.Add(model.NewError(model.CodeManifestMissing, loc(field), "track is missing field"))
			}
			return
		}
		var n json.Number
		if err := json.Unmarshal(v, &n); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc(field), "expected integer").Wrap(err))
			return
		}
		i, err := n.Int64()
		if err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc(field), "expected integer").Wrap(err))
			return
		}
		*dst = i
	}

	var id, ts, start, dur int64 = 0, 0, 0, 0
	getInt("id", &id, true)
	getInt("timescale", &ts, true)
	getInt("start_ticks", &start, true)
	getInt("duration_ticks", &dur, true)
	if id <= 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, loc("id"), "track id must be positive, got %d", id))
	} else {
		t.ID = uint32(id)
	}
	if ts <= 0 || ts > 1_000_000_000 {
		errs.Add(model.NewError(model.CodeManifestInvalid, loc("timescale"), "timescale must be in 1..1000000000, got %d", ts))
	} else {
		t.Timescale = uint32(ts)
	}
	if start < 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, loc("start_ticks"), "start must not be negative, got %d", start))
	}
	t.StartTicks = start
	if dur < 0 {
		errs.Add(model.NewError(model.CodeManifestNegative, loc("duration_ticks"), "duration must not be negative, got %d", dur))
	}
	t.DurationTicks = dur

	if v, ok := fields["kind"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, loc("kind"), "track is missing field"))
	} else {
		var kind string
		if err := json.Unmarshal(v, &kind); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc("kind"), "expected string").Wrap(err))
		} else {
			t.Kind = model.TrackKind(kind)
			if !t.Kind.Valid() {
				errs.Add(model.NewError(model.CodeManifestInvalid, loc("kind"), "unknown track kind %q", kind))
			}
		}
	}

	if v, ok := fields["media_file"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, loc("media_file"), "track is missing field"))
	} else {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc("media_file"), "expected string").Wrap(err))
		} else if s == "" {
			errs.Add(model.NewError(model.CodeManifestInvalid, loc("media_file"), "media file must not be empty"))
		} else if filepath.IsAbs(s) || hasDotDot(s) {
			errs.Add(model.NewError(model.CodeManifestInvalid, loc("media_file"), "media file must stay inside the working directory, got %q", s))
		} else {
			t.MediaFile = s
		}
	}

	// edits
	if v, ok := fields["edit"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, loc("edit"), "track is missing field"))
	} else {
		var edits []json.RawMessage
		if err := json.Unmarshal(v, &edits); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc("edit"), "expected array").Wrap(err))
		}
		for j, er := range edits {
			e, eerrs := parseEdit(er, file, index, t.ID, j, t.Timescale, dur)
			errs = append(errs, eerrs...)
			if eerrs == nil {
				t.Edit = append(t.Edit, e)
			}
		}
		if len(edits) > 0 && len(t.Edit) == len(edits) {
			if err := validateEdits(t.Edit, dur, t.Timescale, file, index, t.ID); err != nil {
				errs.Add(err)
			}
		}
	}

	// keyframes (sparse index in track ticks)
	if v, ok := fields["keyframes"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, loc("keyframes"), "track is missing field"))
	} else {
		var kf []json.Number
		if err := json.Unmarshal(v, &kf); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, loc("keyframes"), "expected integer array").Wrap(err))
		} else {
			prev := int64(-1)
			for j, n := range kf {
				k, err := n.Int64()
				if err != nil {
					errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: file, TrackIndex: index, TrackID: t.ID, Field: fmt.Sprintf("keyframes[%d]", j)}, "expected integer").Wrap(err))
					continue
				}
				if k < 0 {
					errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: file, TrackIndex: index, TrackID: t.ID, Field: fmt.Sprintf("keyframes[%d]", j)}, "keyframe index must not be negative, got %d", k))
					continue
				}
				if k >= dur {
					errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: file, TrackIndex: index, TrackID: t.ID, Field: fmt.Sprintf("keyframes[%d]", j)}, "keyframe %d lies outside track duration %d", k, dur))
					continue
				}
				if k <= prev {
					errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: file, TrackIndex: index, TrackID: t.ID, Field: fmt.Sprintf("keyframes[%d]", j)}, "keyframes must be strictly increasing, got %d after %d", k, prev))
					continue
				}
				prev = k
				t.Keyframes = append(t.Keyframes, k)
			}
			if len(kf) == 0 {
				errs.Add(model.NewError(model.CodeManifestInvalid, loc("keyframes"), "track must expose at least one keyframe"))
			}
		}
	}

	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool {
			a, b := errs[i].Loc, errs[j].Loc
			if a.Field != b.Field {
				return a.Field < b.Field
			}
			return errs[i].Msg < errs[j].Msg
		})
		return nil, errs
	}
	return t, nil
}

func parseEdit(raw json.RawMessage, file string, index int, trackID uint32, j int, timescale uint32, trackDur int64) (Edit, model.ErrorList) {
	var errs model.ErrorList
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d]", j)}, "edit entry is not an object").Wrap(err))
		return Edit{}, errs
	}
	var e Edit
	conv := func(field string, dst *int64) {
		v, ok := fields[field]
		if !ok {
			errs.Add(model.NewError(model.CodeManifestMissing, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].%s", j, field)}, "edit is missing field"))
			return
		}
		var n json.Number
		if err := json.Unmarshal(v, &n); err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].%s", j, field)}, "expected integer").Wrap(err))
			return
		}
		i, err := n.Int64()
		if err != nil {
			errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].%s", j, field)}, "expected integer").Wrap(err))
			return
		}
		*dst = i
	}
	conv("start_ticks", &e.StartTicks)
	conv("duration_ticks", &e.DurationTicks)
	conv("media_time_ticks", &e.MediaTimeTick)
	if e.StartTicks < 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].start_ticks", j)}, "edit start must not be negative, got %d", e.StartTicks))
	}
	if e.DurationTicks < 0 {
		errs.Add(model.NewError(model.CodeManifestNegative, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].duration_ticks", j)}, "edit duration must not be negative, got %d", e.DurationTicks))
	}
	if e.MediaTimeTick < 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].media_time_ticks", j)}, "media time must not be negative, got %d", e.MediaTimeTick))
	}
	if e.StartTicks+e.DurationTicks > trackDur {
		errs.Add(model.NewError(model.CodeManifestEditRange, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d]", j)}, "edit segment ends at %d beyond track duration %d", e.StartTicks+e.DurationTicks, trackDur))
	}
	return e, errs
}

func validateEdits(edits []Edit, trackDur int64, timescale uint32, file string, index int, trackID uint32) *model.Error {
	var prevEnd int64 = -1
	for j, e := range edits {
		if e.StartTicks < prevEnd {
			return model.NewError(model.CodeManifestEditRange, model.Loc{File: file, TrackIndex: index, TrackID: trackID, Field: fmt.Sprintf("edit[%d].start_ticks", j)}, "edit segment starts at %d before previous segment ends at %d", e.StartTicks, prevEnd)
		}
		prevEnd = e.StartTicks + e.DurationTicks
	}
	return nil
}

func hasDotDot(p string) bool {
	for _, part := range filepath.SplitList(p) {
		_ = part
	}
	return bytes.Contains([]byte(p), []byte(".."))
}
