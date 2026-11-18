// Reusable transition kernels. The renderer is the only caller, and the
// self-test exercises the exact same code paths. No floating point is used:
// every mask decision and blend weight is an exact integer rational so all
// platforms round endpoints identically.
#pragma once

#include <cstdint>

#include "timeline.hpp"

namespace tr {

// Per-pixel blend weight returned by every kernel.
//   out = (A * aWeight + B * bWeight) / den,  aWeight + bWeight == den.
struct Weight {
  std::int64_t aWeight;
  std::int64_t bWeight;
  std::int64_t den;

  bool operator==(const Weight& o) const noexcept {
    return aWeight == o.aWeight && bWeight == o.bWeight && den == o.den;
  }
};

// Everything a kernel needs to know about the frame and progress.
struct KernelParams {
  TType type;
  int x;
  int y;
  int width;
  int height;
  std::int64_t progressNum;  // progress = progressNum / progressDen
  std::int64_t progressDen;  // in [0, progressDen] (half-open progress)
};

// Single pixel weight. Progress 0 yields the A clip everywhere; progress
// approaching (but never equal to) 1 yields the B clip everywhere.
Weight pixelWeight(const KernelParams& p);

}  // namespace tr
