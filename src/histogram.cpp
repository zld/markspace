#include "histogram.hpp"

#include <algorithm>
#include <cmath>

#include "errors.hpp"

namespace lutcheck {
namespace {

std::size_t bin_index(double value, int bins) {
  if (!(value > 0.0)) return 0;  // negatives and NaN collapse into bin 0
  if (value >= 1.0) return static_cast<std::size_t>(bins - 1);
  const auto index = static_cast<std::size_t>(value * static_cast<double>(bins));
  return std::min(index, static_cast<std::size_t>(bins - 1));
}

}  // namespace

std::uint64_t Histogram::total() const {
  std::uint64_t sum = 0;
  for (const auto& channel : channels) {
    for (std::uint64_t count : channel) sum += count;
  }
  return sum;
}

Histogram compute_histogram(const Image& image, int bins) {
  if (bins < kMinHistogramBins || bins > kMaxHistogramBins) {
    fail(ExitCode::Usage, "histogram bins must be in [" +
                              std::to_string(kMinHistogramBins) + ", " +
                              std::to_string(kMaxHistogramBins) + "]");
  }
  Histogram histogram;
  histogram.bins = bins;
  for (auto& channel : histogram.channels) {
    channel.assign(static_cast<std::size_t>(bins), 0);
  }
  for (const Vec3& pixel : image.pixels) {
    for (std::size_t channel = 0; channel < 3; ++channel) {
      histogram.channels[channel][bin_index(pixel[channel], bins)] += 1;
    }
  }
  return histogram;
}

std::string histogram_json(const Histogram& histogram) {
  std::string out = "{\"bins\":" + std::to_string(histogram.bins);
  const char* names[3] = {"\"r\":", "\"g\":", "\"b\":"};
  for (std::size_t channel = 0; channel < 3; ++channel) {
    out += ',';
    out += names[channel];
    out += '[';
    const auto& values = histogram.channels[channel];
    for (std::size_t i = 0; i < values.size(); ++i) {
      if (i != 0) out += ',';
      out += std::to_string(values[i]);
    }
    out += ']';
  }
  out += '}';
  return out;
}

}  // namespace lutcheck
