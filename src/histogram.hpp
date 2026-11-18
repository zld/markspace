#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

#include "types.hpp"

namespace lutcheck {

inline constexpr int kMinHistogramBins = 1;
inline constexpr int kMaxHistogramBins = 4096;

// Histograms are computed on the [0, 1] domain with the last bin closed, so
// pure white samples land in the final bin instead of being dropped.
struct Histogram {
  int bins = 0;
  std::array<std::vector<std::uint64_t>, 3> channels;

  std::uint64_t total() const;
};

Histogram compute_histogram(const Image& image, int bins);

// Deterministic, stable JSON: `{"bins":N,"r":[...],"g":[...],"b":[...]}`.
// Counts are integers, so no locale or float formatting can leak in.
std::string histogram_json(const Histogram& histogram);

}  // namespace lutcheck
