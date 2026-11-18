#include "timeline.hpp"

#include <algorithm>
#include <numeric>

#include "errors.hpp"
#include "frame_buffer.hpp"

namespace tr {

namespace {

using i128 = __int128_t;

std::int64_t fit128(i128 v, const std::string& what) {
  if (v > static_cast<i128>(INT64_MAX) ||
      v < static_cast<i128>(INT64_MIN)) {
    throw UsageError("time accumulation overflow while computing " + what);
  }
  return static_cast<std::int64_t>(v);
}

std::int64_t gcd64(std::int64_t a, std::int64_t b) {
  if (a < 0) a = -a;
  if (b < 0) b = -b;
  return static_cast<std::int64_t>(
      std::gcd(static_cast<std::uint64_t>(a),
               static_cast<std::uint64_t>(b)));
}

}  // namespace

Rational::Rational(std::int64_t num, std::int64_t den) {
  if (den == 0) throw UsageError("rational with zero denominator");
  if (den < 0) {
    num = fit128(-static_cast<i128>(num), "rational sign flip");
    den = -den;
  }
  const std::int64_t g = gcd64(num, den);
  num_ = num / g;
  den_ = den / g;
}

Rational Rational::operator+(const Rational& o) const {
  i128 n = static_cast<i128>(num_) * o.den_ +
           static_cast<i128>(o.num_) * den_;
  i128 d = static_cast<i128>(den_) * o.den_;
  return Rational(fit128(n, "rational sum numerator"),
                  fit128(d, "rational sum denominator"));
}

Rational Rational::operator-(const Rational& o) const {
  i128 n = static_cast<i128>(num_) * o.den_ -
           static_cast<i128>(o.num_) * den_;
  i128 d = static_cast<i128>(den_) * o.den_;
  return Rational(fit128(n, "rational difference numerator"),
                  fit128(d, "rational difference denominator"));
}

Rational Rational::operator*(const Rational& o) const {
  i128 n = static_cast<i128>(num_) * o.num_;
  i128 d = static_cast<i128>(den_) * o.den_;
  return Rational(fit128(n, "rational product numerator"),
                  fit128(d, "rational product denominator"));
}

Rational Rational::operator/(const Rational& o) const {
  if (o.num_ == 0) throw UsageError("rational division by zero");
  i128 n = static_cast<i128>(num_) * o.den_;
  i128 d = static_cast<i128>(den_) * o.num_;
  if (d < 0) {
    n = -n;
    d = -d;
  }
  return Rational(fit128(n, "rational quotient numerator"),
                  fit128(d, "rational quotient denominator"));
}

bool Rational::operator==(const Rational& o) const noexcept {
  return num_ == o.num_ && den_ == o.den_;  // both stored reduced
}

bool Rational::operator<(const Rational& o) const noexcept {
  return static_cast<i128>(num_) * o.den_ <
         static_cast<i128>(o.num_) * den_;
}

bool Rational::operator<=(const Rational& o) const noexcept {
  return !(o < *this);
}

std::string Rational::str() const {
  return std::to_string(num_) + "/" + std::to_string(den_);
}

const char* materialName(Material m) {
  switch (m) {
    case Material::Gradient: return "gradient";
    case Material::Checker: return "checker";
    case Material::Coords: return "coords";
  }
  return "unknown";
}

const char* transitionName(TType t) {
  switch (t) {
    case TType::Linear: return "linear";
    case TType::Wipe: return "wipe";
    case TType::Circle: return "circle";
    case TType::CheckerBoard: return "checker_board";
  }
  return "unknown";
}

TType transitionFromName(const std::string& name) {
  if (name == "linear") return TType::Linear;
  if (name == "wipe") return TType::Wipe;
  if (name == "circle") return TType::Circle;
  if (name == "checker_board") return TType::CheckerBoard;
  throw UsageError("unknown transition type: " + name);
}

Rational sourceTimeAt(const Clip& clip, std::int64_t g) {
  Rational t(0, 1);
  for (const SpeedSegment& s : clip.segments) {
    if (g <= s.first) break;
    const std::int64_t take = std::min(g, s.last) - s.first;
    t = t + Rational(take, 1) * s.speed;
  }
  return t;
}

Rational sourceTimeStart(const Clip& clip) {
  return sourceTimeAt(clip, clip.first);
}

void validateTimeline(const Timeline& tl) {
  if (tl.width <= 0 || tl.height <= 0 || tl.width > kMaxDimension ||
      tl.height > kMaxDimension) {
    throw UsageError("invalid frame size in timeline");
  }
  if (!tl.fps.isPositive()) {
    throw UsageError("frame rate must be a positive rational");
  }
  if (tl.clips.empty()) throw UsageError("timeline has no clips");
  if (tl.totalFrames <= 0 || tl.totalFrames > kMaxTimelineFrames) {
    throw UsageError("timeline length must be in 1.." +
                     std::to_string(kMaxTimelineFrames));
  }

  std::int64_t cursor = 0;
  for (std::size_t ci = 0; ci < tl.clips.size(); ++ci) {
    const Clip& c = tl.clips[ci];
    const std::string tag = "clip[" + std::to_string(ci) + "]";
    if (c.first < 0) throw UsageError(tag + ": negative start frame");
    if (c.first != cursor) {
      throw UsageError(tag + ": gap or overlap at frame " +
                       std::to_string(cursor) + " (next starts at " +
                       std::to_string(c.first) + "); intervals are [first,last)");
    }
    if (c.last <= c.first) {
      throw UsageError(tag + ": zero or negative duration");
    }
    if (c.segments.empty()) {
      throw UsageError(tag + ": no speed segments");
    }
    std::int64_t segCursor = c.first;
    for (std::size_t si = 0; si < c.segments.size(); ++si) {
      const SpeedSegment& s = c.segments[si];
      const std::string stag = tag + ".segment[" + std::to_string(si) + "]";
      if (s.first != segCursor) {
        throw UsageError(stag + ": speed segments must tile without gap/overlap");
      }
      if (s.last <= s.first) {
        throw UsageError(stag + ": zero or negative duration at frame " +
                         std::to_string(s.first));
      }
      if (!s.speed.isPositive()) {
        throw UsageError(stag + ": speed must be positive, got " +
                         s.speed.str());
      }
      segCursor = s.last;
    }
    if (segCursor != c.last) {
      throw UsageError(tag + ": speed segments do not reach clip end");
    }
    // Force the accumulation to be representable over the whole clip.
    (void)sourceTimeAt(c, c.last);
    cursor = c.last;
  }
  if (cursor != tl.totalFrames) {
    throw UsageError("clips end at frame " + std::to_string(cursor) +
                     " but totalFrames is " + std::to_string(tl.totalFrames));
  }

  for (std::size_t ti = 0; ti < tl.transitions.size(); ++ti) {
    const Transition& trn = tl.transitions[ti];
    const std::string tag = "transition[" + std::to_string(ti) + "]";
    if (trn.first < 0) throw UsageError(tag + ": negative start frame");
    if (trn.last <= trn.first) {
      throw UsageError(tag + ": zero-duration transition at frame " +
                       std::to_string(trn.first));
    }
    if (trn.last > tl.totalFrames) {
      throw UsageError(tag + ": extends past timeline end");
    }
    if (ti > 0) {
      const Transition& prev = tl.transitions[ti - 1];
      if (trn.first < prev.last) {
        throw UsageError(tag + ": overlaps transition[" +
                         std::to_string(ti - 1) + "] interval");
      }
    }
  }
}

}  // namespace tr
