// histogram.cpp - per-channel histograms over [0, 1] with out-of-range
// counts, computed before and after grading.
#include <cmath>

#include "color_types.hpp"

namespace lutcheck {

namespace {

void add_sample(ChannelHistogram& hist, int bin_count, float value) {
  if (std::isnan(value) || value < 0.0f) {
    ++hist.underflow;  // NaN and -inf land here
  } else if (value > 1.0f || std::isinf(value)) {
    ++hist.overflow;
  } else {
    int idx = static_cast<int>(static_cast<double>(value) * bin_count);
    if (idx >= bin_count) idx = bin_count - 1;  // value == 1.0 inclusive
    ++hist.bins[static_cast<std::size_t>(idx)];
  }
}

}  // namespace

HistogramSet compute_histogram(const std::vector<Rgb>& pixels, int bin_count) {
  HistogramSet set;
  set.bin_count = bin_count;
  for (int c = 0; c < 3; ++c) {
    set.channel[c].bins.assign(static_cast<std::size_t>(bin_count), 0);
  }
  for (const Rgb& p : pixels) {
    add_sample(set.channel[0], bin_count, p.r);
    add_sample(set.channel[1], bin_count, p.g);
    add_sample(set.channel[2], bin_count, p.b);
  }
  return set;
}

}  // namespace lutcheck
