package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediacore/internal/fault"
	"mediacore/internal/scan"
)

func buildPlan(idx *scan.Index, mappings []scan.TrackMapping, samples []scan.SampleRef) ([]plannedSample, int64, string, error) {
	trackByID := map[uint32]scan.Track{}
	for _, t := range idx.Tracks {
		trackByID[t.ID] = t
	}
	mapBySrc := map[uint32]scan.TrackMapping{}
	for _, m := range mappings {
		mapBySrc[m.SourceTrackID] = m
	}
	plan := make([]plannedSample, 0, len(samples))
	for _, s := range samples {
		tr, ok := trackByID[s.TrackID]
		if !ok {
			return nil, 0, "", fault.New(fault.UnsupportedTrack, "sample references unknown track")
		}
		mp, ok := mapBySrc[s.TrackID]
		if !ok {
			return nil, 0, "", fault.New(fault.InvalidArgument, "sample track has no mapping")
		}
		size := int64(s.Size)
		if s.Data != nil {
			size = int64(len(s.Data))
		}
		if size <= 0 || size > 1<<31 {
			return nil, 0, "", fault.New(fault.LimitExceeded, "sample size invalid or too large")
		}
		plan = append(plan, plannedSample{ref: s, track: tr, mapping: mp})
	}
	sort.SliceStable(plan, func(i, j int) bool {
		if plan[i].mapping.OutputTrackID != plan[j].mapping.OutputTrackID {
			return plan[i].mapping.OutputTrackID < plan[j].mapping.OutputTrackID
		}
		if plan[i].ref.DTSNS != plan[j].ref.DTSNS {
			return plan[i].ref.DTSNS < plan[j].ref.DTSNS
		}
		return plan[i].ref.Index < plan[j].ref.Index
	})
	var total int64
	h := sha256.New()
	for i := range plan {
		plan[i].offset = uint32(32 + total)
		size := int64(plan[i].ref.Size)
		if plan[i].ref.Data != nil {
			size = int64(len(plan[i].ref.Data))
			plan[i].ref.Size = uint32(size)
		}
		total += size
		_, _ = h.Write([]byte(plan[i].track.Codec))
		_, _ = h.Write([]byte{byte(plan[i].ref.TrackID), byte(plan[i].ref.Size), byte(plan[i].ref.DTSNS)})
	}
	return plan, total, hex.EncodeToString(h.Sum(nil)), nil
}

func ensureAllowed(root, target string) error {
	if root == "" {
		return nil
	}
	ar, err := filepath.Abs(root)
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	at, err := filepath.Abs(target)
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	rel, err := filepath.Rel(ar, at)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fault.New(fault.InvalidArgument, "refusing to write outside allowed root: "+target)
	}
	return nil
}
