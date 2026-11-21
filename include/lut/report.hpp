#ifndef LUT_REPORT_HPP
#define LUT_REPORT_HPP

#include <array>
#include <cstdint>
#include <vector>

#include "lut/model.hpp"

namespace lut {

inline constexpr int kHistogramBuckets = 16;

using Histogram = std::array<std::uint64_t, kHistogramBuckets>;

// Per-channel maximum absolute deviation between two sample sets.
struct Deviation {
    RGB maxAbs{0.0, 0.0, 0.0};
    std::size_t count = 0;

    // Largest of the three channel maxima, used against --max-error.
    double worst() const;
};

// Histograms plus pixel counters. Both counters use the same unit: a pixel
// is counted once when any channel is outside the domain (before grading)
// or clipped back into [0,1] (after encoding).
struct ImageStats {
    std::array<Histogram, 3> before{};
    std::array<Histogram, 3> after{};
    std::uint64_t outOfDomain = 0;
    std::uint64_t clipped = 0;

    std::uint64_t beforeTotal(int channel) const;
    std::uint64_t afterTotal(int channel) const;
};

// Compares matched reference/baked probe samples (same ordering).
Deviation measureDeviation(const std::vector<RGB>& reference,
                           const std::vector<RGB>& baked);

// Accumulates one encoded RGB value into one channel's histogram. Values in
// [0,1] map to the 16 buckets; anything outside is left to the caller's
// out-of-domain / clipped counters.
void accumulateHistogram(Histogram& hist, double value);

}  // namespace lut

#endif  // LUT_REPORT_HPP
