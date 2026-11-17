#include "timeline.hpp"

#include <string>

#include "errors.hpp"
#include "frame_buffer.hpp"

namespace tl {

namespace {

int64_t gcd_i64(int64_t a, int64_t b) {
  if (a < 0) a = -a;
  if (b < 0) b = -b;
  while (b) {
    int64_t t = a % b;
    a = b;
    b = t;
  }
  return a;
}

int64_t parse_digits(const std::string& s, size_t begin, size_t end,
                     const std::string& text, const char* what) {
  if (begin == end || end - begin > 10)
    throw ArgError(std::string("invalid ") + what + " '" + text + "'");
  int64_t v = 0;
  for (size_t i = begin; i < end; ++i) {
    if (s[i] < '0' || s[i] > '9')
      throw ArgError(std::string("invalid ") + what + " '" + text + "'");
    v = v * 10 + (s[i] - '0');
  }
  return v;
}

}  // namespace

Rational parse_rational(const std::string& text, const char* what) {
  size_t pos = 0;
  bool neg = false;
  if (pos < text.size() && (text[pos] == '-' || text[pos] == '+')) {
    neg = text[pos] == '-';
    ++pos;
  }
  const size_t slash = text.find('/', pos);
  const int64_t num =
      parse_digits(text, pos, slash == std::string::npos ? text.size() : slash,
                   text, what);
  int64_t den = 1;
  if (slash != std::string::npos) {
    den = parse_digits(text, slash + 1, text.size(), text, what);
    if (den == 0)
      throw ArgError(std::string("invalid ") + what + " '" + text +
                     "': zero denominator");
  }
  Rational r{(neg ? -num : num), den};
  const int64_t g = gcd_i64(r.num, r.den);
  r.num /= g;
  r.den /= g;
  return r;
}

int64_t frames_for_duration(Rational duration, Rational fps) {
  if (duration.num <= 0)
    throw ArgError("duration must be positive, got " +
                   std::to_string(duration.num) + "/" +
                   std::to_string(duration.den));
  if (fps.num <= 0)
    throw ArgError("fps must be positive, got " + std::to_string(fps.num) +
                   "/" + std::to_string(fps.den));
  const __int128 n = static_cast<__int128>(duration.num) * fps.num;
  const __int128 d = static_cast<__int128>(duration.den) * fps.den;
  const __int128 frames = (2 * n + d) / (2 * d);  // round half-up
  if (frames < 1 || frames > kMaxFramesPerSequence)
    throw ArgError("frame count out of range (1.." +
                   std::to_string(kMaxFramesPerSequence) + ") for duration " +
                   std::to_string(duration.num) + "/" +
                   std::to_string(duration.den) + " at fps " +
                   std::to_string(fps.num) + "/" + std::to_string(fps.den));
  return static_cast<int64_t>(frames);
}

Timeline::Timeline(Rational fps, int64_t total_frames,
                   std::vector<SpeedPiece> pieces)
    : fps_(fps), total_frames_(total_frames), pieces_(std::move(pieces)) {
  if (fps_.num <= 0) throw ArgError("timeline: fps must be positive");
  if (total_frames_ < 1 || total_frames_ > kMaxFramesPerSequence)
    throw ArgError("timeline: frame count " + std::to_string(total_frames_) +
                   " out of range (1.." +
                   std::to_string(kMaxFramesPerSequence) + ")");
  if (pieces_.empty()) throw ArgError("timeline: no speed pieces");

  // Common scale for exact content-time arithmetic (bounded to keep the
  // downstream fixed-point products inside int64).
  constexpr int64_t kMaxScale = 1000000;
  int64_t scale = 1;
  for (size_t i = 0; i < pieces_.size(); ++i) {
    const int64_t d = pieces_[i].speed.den;
    const __int128 next =
        static_cast<__int128>(scale) * (d / gcd_i64(scale, d));
    if (next > kMaxScale)
      throw ArgError("timeline: speed denominators overflow at piece " +
                     std::to_string(i));
    scale = static_cast<int64_t>(next);
  }
  content_scale_ = scale;

  content_prefix_.resize(pieces_.size());
  int64_t expect_begin = 0;
  __int128 acc = 0;
  for (size_t i = 0; i < pieces_.size(); ++i) {
    const SpeedPiece& p = pieces_[i];
    const std::string where = "timeline piece " + std::to_string(i);
    if (p.speed.num < 0)
      throw ArgError(where + ": negative speed " +
                     std::to_string(p.speed.num) + "/" +
                     std::to_string(p.speed.den));
    if (p.speed.num > 4 * p.speed.den)
      throw ArgError(where + ": speed " + std::to_string(p.speed.num) + "/" +
                     std::to_string(p.speed.den) + " exceeds 4x");
    if (p.end_frame <= p.begin_frame)
      throw ArgError(where + ": invalid interval [" +
                     std::to_string(p.begin_frame) + ", " +
                     std::to_string(p.end_frame) + ")");
    if (p.begin_frame != expect_begin)
      throw ArgError(where + ": gap or overlap at frame " +
                     std::to_string(expect_begin) + " (piece starts at " +
                     std::to_string(p.begin_frame) + ")");
    content_prefix_[i] = static_cast<int64_t>(acc);
    const __int128 step = static_cast<__int128>(p.speed.num) *
                          (content_scale_ / p.speed.den);
    acc += static_cast<__int128>(p.end_frame - p.begin_frame) * step;
    if (acc > (static_cast<__int128>(1) << 62))
      throw ArgError(where + ": content timestamp overflow");
    expect_begin = p.end_frame;
  }
  if (expect_begin != total_frames_)
    throw ArgError("timeline: pieces end at frame " +
                   std::to_string(expect_begin) + ", expected " +
                   std::to_string(total_frames_));
  // Content time of the last frame (not one past it).
  const SpeedPiece& last = pieces_.back();
  const int64_t last_step =
      last.speed.num * (content_scale_ / last.speed.den);
  content_total_ =
      content_prefix_.back() + (total_frames_ - 1 - last.begin_frame) * last_step;
  if (total_frames_ > 1 && content_total_ <= 0)
    throw ArgError("timeline: no forward progress (all speeds are zero)");
}

Rational Timeline::progress(int64_t frame_index) const {
  if (frame_index < 0 || frame_index >= total_frames_)
    throw ArgError("timeline: frame index " + std::to_string(frame_index) +
                   " outside [0, " + std::to_string(total_frames_) + ")");
  if (total_frames_ == 1) return Rational{1, 1};
  size_t p = 0;
  while (p + 1 < pieces_.size() && frame_index >= pieces_[p + 1].begin_frame)
    ++p;
  const SpeedPiece& piece = pieces_[p];
  const int64_t step = piece.speed.num * (content_scale_ / piece.speed.den);
  const int64_t content =
      content_prefix_[p] + (frame_index - piece.begin_frame) * step;
  const int64_t g = gcd_i64(content, content_total_);
  return Rational{content / g, content_total_ / g};
}

}  // namespace tl
