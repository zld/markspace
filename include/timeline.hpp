#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace tl {

// Normalized rational (den > 0, gcd-reduced).
struct Rational {
  int64_t num = 0;
  int64_t den = 1;
};

// Parses "num/den" or a plain integer. |num|, den <= 1e9.
// Throws ArgError naming `what` and the offending text.
Rational parse_rational(const std::string& text, const char* what);

// frames = round(duration_seconds * fps), half-up. Result is in
// [1, kMaxFramesPerSequence]. Throws ArgError on overflow/out of range.
int64_t frames_for_duration(Rational duration, Rational fps);

// Half-open speed piece over [begin_frame, end_frame); speed in [0, 4].
struct SpeedPiece {
  int64_t begin_frame = 0;
  int64_t end_frame = 0;
  Rational speed{1, 1};
};

// Maps frame indices to transition progress under piecewise-constant
// speed. Pieces must tile [0, total_frames) contiguously: no gaps, no
// overlaps, so the timeline yields neither duplicate frames nor holes.
class Timeline {
 public:
  // Throws ArgError (with the offending piece index) on negative or >4x
  // speed, invalid/unsorted intervals, gaps, or timestamp overflow.
  Timeline(Rational fps, int64_t total_frames, std::vector<SpeedPiece> pieces);

  int64_t total_frames() const { return total_frames_; }
  Rational fps() const { return fps_; }

  // Progress of frame i in [0,1]: frame 0 -> 0, last frame -> 1.
  Rational progress(int64_t frame_index) const;

 private:
  Rational fps_;
  int64_t total_frames_ = 0;
  std::vector<SpeedPiece> pieces_;
  int64_t content_scale_ = 1;             // common denominator for content time
  std::vector<int64_t> content_prefix_;   // scaled content time at piece starts
  int64_t content_total_ = 0;             // scaled content time of last frame
};

}  // namespace tl
