#include "fmp4/validator.hpp"

#include <algorithm>
#include <limits>
#include <set>

namespace fmp4 {

const TrackTimeline *ValidationReport::timeline(uint32_t track_id) const {
  for (const auto &t : timelines)
    if (t.track_id == track_id) return &t;
  return nullptr;
}

double ticks_to_seconds(uint64_t ticks, uint32_t timescale) {
  if (timescale == 0) return 0.0;
  return double(ticks) / double(timescale);
}

const char *issue_kind_name(IssueKind k) {
  switch (k) {
  case IssueKind::MissingFragment: return "missing_fragment";
  case IssueKind::DuplicateSequence: return "duplicate_sequence";
  case IssueKind::CorruptBox: return "corrupt_box";
  case IssueKind::UnknownTrack: return "unknown_track";
  case IssueKind::IncompatibleSampleEntry:
    return "incompatible_sample_entry";
  case IssueKind::MfhdGap: return "mfhd_gap";
  case IssueKind::MfhdDuplicate: return "mfhd_duplicate";
  case IssueKind::TrackTimeline: return "track_timeline";
  }
  return "unknown";
}

static TrackTimeline &get_timeline(std::vector<TrackTimeline> &v,
                                   uint32_t track_id, uint32_t timescale) {
  for (auto &t : v)
    if (t.track_id == track_id) return t;
  TrackTimeline t;
  t.track_id = track_id;
  t.timescale = timescale;
  t.decode_start = std::numeric_limits<uint64_t>::max();
  v.push_back(t);
  return v.back();
}

ValidationReport validate_index(const FragmentIndex &idx) {
  ValidationReport rep;
  for (uint32_t seq : idx.missing_sequences) {
    const FragmentInfo *f = idx.find_manifest(seq);
    rep.issues.push_back({IssueKind::MissingFragment,
                          f ? f->rel_path : std::string(), 0, seq,
                          "fragment file missing for manifest sequence " +
                              std::to_string(seq)});
  }
  for (uint32_t seq : idx.duplicate_sequences)
    rep.issues.push_back({IssueKind::DuplicateSequence,
                          std::string("fragments.manifest"), 0, seq,
                          "duplicate manifest sequence " +
                              std::to_string(seq)});

  // mfhd continuity over present fragments, in manifest order.
  std::set<uint32_t> seen_mfhd;
  uint32_t lo = std::numeric_limits<uint32_t>::max(), hi = 0;
  bool any = false;
  for (const auto &f : idx.fragments) {
    if (!f.present) continue;
    any = true;
    rep.mfhd_sequences.push_back(f.mfhd_sequence);
    if (!seen_mfhd.insert(f.mfhd_sequence).second)
      rep.issues.push_back(
          {IssueKind::MfhdDuplicate, f.rel_path, f.mfhd_offset,
           f.manifest_seq,
           "duplicate mfhd sequence " +
               std::to_string(f.mfhd_sequence)});
    lo = std::min(lo, f.mfhd_sequence);
    hi = std::max(hi, f.mfhd_sequence);
  }
  if (any) {
    for (uint32_t v = lo; v < hi; ++v) {
      if (!seen_mfhd.count(v)) {
        rep.mfhd_gaps.push_back(v);
        rep.issues.push_back(
            {IssueKind::MfhdGap, std::string(), 0, 0,
             "mfhd sequence " + std::to_string(v) + " missing from run"});
      }
      if (v == std::numeric_limits<uint32_t>::max() - 1) break;
    }
    rep.mfhd_contiguous = rep.mfhd_gaps.empty();
  }

  // Per-track decode timelines over present, parseable fragments.
  for (const auto &f : idx.fragments) {
    if (!f.present) continue;
    for (const auto &run : f.tracks) {
      const TrackInit *ti = idx.find_track(run.track_id);
      uint32_t scale = ti ? ti->timescale : 0;
      auto &tl = get_timeline(rep.timelines, run.track_id, scale);
      if (!ti)
        rep.issues.push_back({IssueKind::UnknownTrack, f.rel_path,
                               f.moof_offset, f.manifest_seq,
                               "traf references unknown track id " +
                                   std::to_string(run.track_id)});
      tl.decode_start = std::min(tl.decode_start, run.decode_start);
      tl.total_duration += run.duration;
      ++tl.fragments_present;
    }
  }
  for (auto &tl : rep.timelines)
    if (tl.decode_start == std::numeric_limits<uint64_t>::max())
      tl.decode_start = 0;
  std::sort(rep.timelines.begin(), rep.timelines.end(),
            [](const TrackTimeline &a, const TrackTimeline &b) {
              return a.track_id < b.track_id;
            });
  return rep;
}

static int64_t delta(uint64_t a, uint64_t b) {
  // Saturating signed difference.
  if (a >= b) {
    uint64_t d = a - b;
    return d > uint64_t(std::numeric_limits<int64_t>::max())
               ? std::numeric_limits<int64_t>::max()
               : int64_t(d);
  }
  uint64_t d = b - a;
  return d > uint64_t(std::numeric_limits<int64_t>::max())
             ? std::numeric_limits<int64_t>::min()
             : -int64_t(d);
}

ValidationReport validate_against_baseline(const FragmentIndex &out_idx,
                                           const FragmentIndex &base_idx) {
  ValidationReport rep = validate_index(out_idx);

  // Init segment compatibility: every baseline track must survive with the
  // same timescale, handler and sample entry.
  for (const auto &bt : base_idx.init.tracks) {
    const TrackInit *ot = nullptr;
    for (const auto &t : out_idx.init.tracks)
      if (t.track_id == bt.track_id) ot = &t;
    if (!ot) {
      rep.issues.push_back({IssueKind::UnknownTrack, out_idx.init.path, 0, 0,
                            "output init missing track id " +
                                std::to_string(bt.track_id)});
      continue;
    }
    if (ot->sample_entry != bt.sample_entry || ot->handler != bt.handler ||
        ot->timescale != bt.timescale) {
      rep.issues.push_back(
          {IssueKind::IncompatibleSampleEntry, out_idx.init.path, 0, 0,
           "track " + std::to_string(bt.track_id) +
               " init parameters drift from baseline"});
    }
  }

  ValidationReport base_rep = validate_index(base_idx);
  for (const auto &btl : base_rep.timelines) {
    const TrackTimeline *otl = rep.timeline(btl.track_id);
    auto &dst = get_timeline(rep.timelines, btl.track_id, btl.timescale);
    if (!otl) {
      rep.issues.push_back({IssueKind::TrackTimeline, out_idx.root_dir, 0, 0,
                            "output has no samples for baseline track " +
                                std::to_string(btl.track_id)});
      continue;
    }
    dst.decode_start_delta = delta(otl->decode_start, btl.decode_start);
    dst.duration_delta = delta(otl->total_duration, btl.total_duration);
    if (dst.decode_start_delta != 0 || dst.duration_delta != 0)
      rep.issues.push_back(
          {IssueKind::TrackTimeline, out_idx.root_dir, 0, 0,
           "track " + std::to_string(btl.track_id) + " timeline drift"});
  }
  std::sort(rep.timelines.begin(), rep.timelines.end(),
            [](const TrackTimeline &a, const TrackTimeline &b) {
              return a.track_id < b.track_id;
            });
  return rep;
}

} // namespace fmp4
