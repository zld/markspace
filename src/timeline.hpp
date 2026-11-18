// Timeline model: integer frame indices, rational frame rate, rational
// variable speeds. Every interval is half-open [first, last): consecutive
// pieces therefore neither repeat nor skip frames.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace tr {

// Exact non-negative rational. Stored reduced, denominator always positive.
// Arithmetic overflows throw UsageError (via __int128 overflow detection).
class Rational {
 public:
  Rational() : num_(0), den_(1) {}
  Rational(std::int64_t num, std::int64_t den);

  std::int64_t num() const noexcept { return num_; }
  std::int64_t den() const noexcept { return den_; }

  bool isZero() const noexcept { return num_ == 0; }
  bool isPositive() const noexcept { return num_ > 0; }

  Rational operator+(const Rational& o) const;
  Rational operator-(const Rational& o) const;
  Rational operator*(const Rational& o) const;
  Rational operator/(const Rational& o) const;  // divisor must be non-zero
  bool operator==(const Rational& o) const noexcept;
  bool operator!=(const Rational& o) const noexcept { return !(*this == o); }
  bool operator<(const Rational& o) const noexcept;
  bool operator<=(const Rational& o) const noexcept;

  std::string str() const;

 private:
  std::int64_t num_;
  std::int64_t den_;
};

enum class Material { Gradient, Checker, Coords };
enum class TType { Linear, Wipe, Circle, CheckerBoard };

const char* materialName(Material m);
const char* transitionName(TType t);
TType transitionFromName(const std::string& name);

// One constant-speed stretch of a clip, half-open [first, last) in global
// frames. `speed` is source frames advanced per global frame.
struct SpeedSegment {
  std::int64_t first;
  std::int64_t last;
  Rational speed;
};

struct Clip {
  std::int64_t first;  // inclusive global frame
  std::int64_t last;   // exclusive global frame
  Material material;
  std::vector<SpeedSegment> segments;  // must tile [first, last)
};

// Half-open overlay interval [first, last); zero length is rejected.
struct Transition {
  std::int64_t first;
  std::int64_t last;
  TType type;
};

// Upper bound so all source-time products fit in int64 with headroom.
constexpr std::int64_t kMaxTimelineFrames = 10'000'000;

struct Timeline {
  int width = 0;
  int height = 0;
  Rational fps;                       // frames per second, > 0
  std::vector<Clip> clips;            // ordered, tile [0, totalFrames)
  std::vector<Transition> transitions;
  std::int64_t totalFrames = 0;
};

// Full structural validation. Every error message names the offending
// clip/transition/segment index and frame position. Throws UsageError.
void validateTimeline(const Timeline& tl);

// Source time (in source frames, may be fractional) for global frame `g`,
// accumulating across speed segments. Throws on unrepresentable totals.
Rational sourceTimeAt(const Clip& clip, std::int64_t g);

// Source time at the first frame of a clip (0 by construction of validation).
Rational sourceTimeStart(const Clip& clip);

}  // namespace tr
