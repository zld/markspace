#pragma once

#include <stdexcept>
#include <vector>

#include "manifest.hpp"

namespace tl {

// Hard cap on total output frames.
constexpr int64_t kMaxFrames = 100000;

// Thrown when the manifest parses but the timeline is illegal (exit 3).
// The message names the offending clip/transition and frame position.
struct TimelineError : std::runtime_error {
  using std::runtime_error::runtime_error;
};

// Where one output frame comes from.
struct FrameSource {
  int clip_a = 0;
  int64_t src_a = 0;  // source frame index inside clip A
  bool in_transition = false;
  int clip_b = 0;
  int64_t src_b = 0;               // source frame index inside clip B
  int transition_index = -1;       // index into Manifest::transitions
  int64_t transition_frame = 0;    // k in [0, T)
  Rational progress{0, 1};         // k / (T - 1), exact
};

// Validated timeline: rational frame clock, speed mapping, region lookup.
class Timeline {
 public:
  explicit Timeline(Manifest m);  // validates; throws TimelineError

  int64_t total_frames() const { return total_; }
  const Manifest& manifest() const { return m_; }

  // Maps a global output frame index to its source(s).
  FrameSource map(int64_t out_frame) const;

  // Output frame index at which transition t's region begins.
  int64_t transition_start(int t) const { return trans_start_.at(t); }

  // Clip-local output frame -> source frame index via the speed segments.
  // Exact rational accumulation; result is floor(position).
  static int64_t clip_source_frame(const Clip& c, int64_t local);

 private:
  Manifest m_;
  int64_t total_ = 0;
  std::vector<int64_t> clip_start_;   // output offset of clip local frame 0
  std::vector<int64_t> trans_start_;  // output offset of each transition
};

}  // namespace tl
