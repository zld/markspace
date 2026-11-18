#include "hash.hpp"

#include <array>

#include "errors.hpp"

namespace tr {

namespace {
constexpr std::uint64_t kFnvOffset = 0xcbf29ce484222325ULL;
constexpr std::uint64_t kFnvPrime = 0x100000001b3ULL;
}  // namespace

std::uint64_t hashFrame(const FrameBuffer& fb) {
  std::uint64_t h = kFnvOffset;
  for (std::uint8_t byte : fb.bytes()) {
    h ^= byte;
    h *= kFnvPrime;  // unsigned multiply wraps mod 2^64 by definition
  }
  return h;
}

std::string toHex64(std::uint64_t value) {
  static constexpr char kDigits[] = "0123456789abcdef";
  std::array<char, 16> buf{};
  for (int i = 15; i >= 0; --i) {
    buf[static_cast<std::size_t>(i)] = kDigits[value & 0xFu];
    value >>= 4;
  }
  return std::string(buf.data(), buf.size());
}

FrameDiff compareFrames(const FrameBuffer& a, const FrameBuffer& b) {
  if (!a.sameSize(b)) {
    throw UsageError("cannot compare frames of different dimensions");
  }
  FrameDiff result{0, 0};
  const auto& x = a.bytes();
  const auto& y = b.bytes();
  for (std::size_t p = 0; p < x.size(); p += 3) {
    bool different = false;
    for (int c = 0; c < 3; ++c) {
      const int d = (x[p + c] >= y[p + c])
                        ? static_cast<int>(x[p + c] - y[p + c])
                        : static_cast<int>(y[p + c] - x[p + c]);
      if (d != 0) different = true;
      result.sumAbsDiff += static_cast<std::uint64_t>(d);
    }
    if (different) ++result.diffPixels;
  }
  return result;
}

std::string formatMeanAbs(const FrameDiff& diff, int width, int height) {
  // mean = sum / (3*width*height); render 4 fractional digits by scaling
  // the integer numerator by 10000 before dividing.
  const std::uint64_t channels =
      3ULL * static_cast<std::uint64_t>(width) *
      static_cast<std::uint64_t>(height);
  if (channels == 0 || diff.sumAbsDiff == 0) return "0.0000";
  const std::uint64_t scaled = diff.sumAbsDiff * 10000ULL;
  const std::uint64_t q = scaled / channels;
  const std::uint64_t whole = q / 10000ULL;
  const std::uint64_t frac = q % 10000ULL;
  std::string s = std::to_string(whole) + ".";
  std::string f = std::to_string(frac);
  s.append(4u - f.size(), '0');
  s += f;
  return s;
}

}  // namespace tr
