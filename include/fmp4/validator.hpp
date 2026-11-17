#pragma once
// Validator: checks a FragmentIndex against structural and compatibility
// rules. All findings carry file + byte position; the validator never throws
// for content errors, it accumulates them so a single run reports everything.
#include <cstdint>
#include <string>
#include <vector>

#include "fmp4/fragment_index.hpp"

namespace fmp4 {

enum class IssueKind {
  MissingFragment,
  DuplicateSequence,
  CorruptBox,
  UnknownTrack,
  IncompatibleSampleEntry,
  MfhdGap,
  MfhdDuplicate,
  TrackTimeline,
};

struct Issue {
  IssueKind kind = IssueKind::CorruptBox;
  std::string file;
  uint64_t offset = 0;
  uint32_t sequence = 0; // manifest sequence when applicable
  std::string detail;
};

struct TrackTimeline {
  uint32_t track_id = 0;
  uint32_t timescale = 0;
  uint64_t decode_start = 0; // first tfdt seen, ticks
  uint64_t total_duration = 0; // summed across present fragments, ticks
  uint32_t fragments_present = 0;
  // Populated by validate_against_baseline; signed to expose drift direction.
  int64_t decode_start_delta = 0;
  int64_t duration_delta = 0;
};

struct ValidationReport {
  std::vector<Issue> issues;
  std::vector<TrackTimeline> timelines;
  std::vector<uint32_t> mfhd_sequences; // in manifest order, present only
  std::vector<uint32_t> mfhd_gaps;      // missing values in the mfhd run
  bool mfhd_contiguous = true;

  bool ok() const { return issues.empty(); }
  const TrackTimeline *timeline(uint32_t track_id) const;
};

// Validate an index against its own embedded init segment.
ValidationReport validate_index(const FragmentIndex &idx);

// Compare a repacked/output index against a baseline input index.
// Per-track decode-start and total-duration deltas are recorded in `out`
// (ticks plus seconds); any non-zero delta or count mismatch is an issue.
ValidationReport validate_against_baseline(const FragmentIndex &out_idx,
                                           const FragmentIndex &base_idx);

double ticks_to_seconds(uint64_t ticks, uint32_t timescale);

const char *issue_kind_name(IssueKind k);

} // namespace fmp4
