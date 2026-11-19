#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

#include "color/types.hpp"

namespace color {

struct Report {
    std::string inputSpace = "srgb";
    std::string lutSpace = "linear-rgb";
    std::string boundary = "clamp";
    int lutSize = 0;
    int lutDims[3] = {0, 0, 0};
    std::size_t pixels = 0;
    int threads = 1;
    std::uint64_t seed = 0;
    RGB maxAbsDiff{0.0, 0.0, 0.0};
    ChannelHistograms before;
    ChannelHistograms after;
};

// One-line digest embedded in the JSON report ("summary" field). It repeats
// the scalar facts in `key:value` form so shell checks can grep either the
// JSON fields or this digest without parsing.
std::string reportDigest(const Report& report);

// JSON and text encoders. Both terminate with a newline and are byte-stable
// for equal reports, so the CLI summary can be diffed across runs.
std::string renderJson(const Report& report);
std::string renderText(const Report& report);

// Recomputes all histogram totals and cross-checks them against the pixel
// count. Returns a human-readable problem, or an empty string when valid.
std::string validateCounts(const Report& report);

}  // namespace color
