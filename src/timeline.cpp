#include "timeline.hpp"

#include <cinttypes>
#include <cstdio>

#include "frame_buffer.hpp"  // kMaxSide

namespace tl {

namespace {

[[noreturn]] void fail(const char* what, int index, int64_t frame,
                       const std::string& msg) {
  char buf[256];
  std::snprintf(buf, sizeof buf, "%s %d (frame %" PRId64 "): %s", what, index,
                frame, msg.c_str());
  throw TimelineError(buf);
}

void validate_speeds(const Clip& c, int clip_index) {
  int64_t prev_end = 0;
  for (size_t i = 0; i < c.speeds.size(); ++i) {
    const SpeedSeg& s = c.speeds[i];
    if (s.end <= s.start)
      fail("clip", clip_index, s.start, "speed segment is empty");
    if (s.start < 0 || s.end > c.frames)
      fail("clip", clip_index, s.start, "speed segment out of clip range");
    if (i > 0 && s.start < prev_end)
      fail("clip", clip_index, s.start, "speed segments overlap");
    prev_end = s.end;
    // speed must be within [1/4, 4]
    if (s.speed.num <= 0 || 4 * s.speed.num < s.speed.den ||
        s.speed.num > 4 * s.speed.den)
      fail("clip", clip_index, s.start, "speed outside [1/4, 4]");
    // keep denominators bounded so exact accumulation cannot overflow
    if (s.speed.num > 1000000 || s.speed.den > 1000000)
      fail("clip", clip_index, s.start,
           "speed ratio too large to map exactly");
  }
}

}  // namespace

Timeline::Timeline(Manifest m) : m_(std::move(m)) {
  if (m_.width == 0 || m_.height == 0 || m_.width > kMaxSide ||
      m_.height > kMaxSide)
    throw TimelineError("canvas dimensions out of range");
  if (m_.clips.empty()) throw TimelineError("timeline has no clips");
  if (m_.transitions.size() + 1 != m_.clips.size())
    throw TimelineError("clip/transition order invalid: need clip "
                        "(transition clip)*, got " +
                        std::to_string(m_.clips.size()) + " clips and " +
                        std::to_string(m_.transitions.size()) + " transitions");

  const size_t n = m_.clips.size();
  clip_start_.assign(n, 0);
  trans_start_.assign(n - 1, 0);

  __int128 acc = 0;  // overflow-proof accumulator for frame counts
  for (size_t i = 0; i < n; ++i) {
    const Clip& c = m_.clips[i];
    if (c.frames <= 0)
      fail("clip", static_cast<int>(i), 0, "clip has no frames");
    if (c.frames > kMaxFrames)
      fail("clip", static_cast<int>(i), c.frames, "clip exceeds frame cap");
    validate_speeds(c, static_cast<int>(i));
    clip_start_[i] = static_cast<int64_t>(acc);
    acc += c.frames;
    if (i < n - 1) {
      const Transition& t = m_.transitions[i];
      if (t.frames < 2)
        fail("transition", static_cast<int>(i), t.frames,
             "transition needs at least 2 frames");
      if (t.frames > c.frames || t.frames > m_.clips[i + 1].frames)
        fail("transition", static_cast<int>(i), t.frames,
             "transition overlaps beyond an adjacent clip");
      acc -= t.frames;  // transition region reuses the tail of clip i
      trans_start_[i] = static_cast<int64_t>(acc);
    }
    if (acc > kMaxFrames)
      throw TimelineError("total frame count exceeds cap of " +
                          std::to_string(kMaxFrames));
  }
  total_ = static_cast<int64_t>(acc);
}

int64_t Timeline::clip_source_frame(const Clip& c, int64_t local) {
  // position = sum over covered frames of the segment speed (default 1).
  __int128 num = 0, den = 1;
  auto add = [&](int64_t count, int64_t sn, int64_t sd) {
    if (count <= 0) return;
    num = num * sd + static_cast<__int128>(count) * sn * den;
    den = den * sd;
  };
  int64_t cursor = 0;
  for (const SpeedSeg& s : c.speeds) {
    if (s.start >= local) break;
    add(s.start - cursor, 1, 1);  // gap before the segment runs at 1x
    int64_t covered = (s.end < local ? s.end : local) - s.start;
    add(covered, s.speed.num, s.speed.den);
    cursor = s.end < local ? s.end : local;
  }
  add(local - cursor, 1, 1);  // trailing gap
  return static_cast<int64_t>(num / den);  // floor, position is non-negative
}

FrameSource Timeline::map(int64_t out_frame) const {
  if (out_frame < 0 || out_frame >= total_)
    throw TimelineError("output frame out of range: " +
                        std::to_string(out_frame));
  FrameSource fs;
  const size_t n = m_.clips.size();
  for (size_t i = 0; i < n; ++i) {
    const Clip& c = m_.clips[i];
    int64_t base = clip_start_[i];
    int64_t local = out_frame - base;
    if (local < 0 || local >= c.frames) continue;
    fs.clip_a = static_cast<int>(i);
    fs.src_a = clip_source_frame(c, local);
    if (i < n - 1) {
      const Transition& t = m_.transitions[i];
      int64_t k = out_frame - trans_start_[i];
      if (k >= 0 && k < t.frames) {
        fs.in_transition = true;
        fs.clip_b = static_cast<int>(i + 1);
        fs.src_b = clip_source_frame(m_.clips[i + 1], k);
        fs.transition_index = static_cast<int>(i);
        fs.transition_frame = k;
        fs.progress = make_rational(k, t.frames - 1);
      }
    }
    return fs;
  }
  throw TimelineError("output frame maps to no clip: " +
                      std::to_string(out_frame));
}

}  // namespace tl
