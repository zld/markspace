#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace tl {

// Reduced rational with positive denominator.
struct Rational {
  int64_t num = 0;
  int64_t den = 1;
};

// Reduces num/den; throws std::invalid_argument on den == 0.
Rational make_rational(int64_t num, int64_t den);

enum class GenType { Gradient, Checker, Shapes };
enum class KernelType { Crossfade, Wipe, Radial };

// Half-open clip-local output frame range [start, end) played at `speed`.
struct SpeedSeg {
  int64_t start = 0;
  int64_t end = 0;
  Rational speed{1, 1};
};

struct Clip {
  GenType gen = GenType::Gradient;
  int64_t frames = 0;
  std::vector<SpeedSeg> speeds;
};

struct Transition {
  KernelType kernel = KernelType::Crossfade;
  int64_t frames = 0;
};

// Timeline description. The manifest never names input paths; all source
// material is procedural. transitions[i] joins clips[i] and clips[i+1].
struct Manifest {
  uint32_t width = 0;
  uint32_t height = 0;
  Rational fps{24, 1};
  std::vector<Clip> clips;
  std::vector<Transition> transitions;
};

const char* to_string(GenType t);
const char* to_string(KernelType t);

// Parses manifest text. On failure returns false and sets err to a message
// including the 1-based line number. Syntax errors only; semantic timeline
// validation happens in Timeline (exit code 3 territory).
bool parse_manifest(const std::string& text, Manifest& out, std::string& err);

}  // namespace tl
