// color_types.hpp - core pixel, matrix, LUT and statistics types shared by
// every module of lutcheck.
#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace lutcheck {

// A single RGB pixel: three float channels, no alpha.
struct Rgb {
  float r = 0.0f;
  float g = 0.0f;
  float b = 0.0f;
};

// Row-major 3x3 matrix in double precision for color-space conversion.
struct Mat3 {
  double m[3][3] = {{0.0, 0.0, 0.0}, {0.0, 0.0, 0.0}, {0.0, 0.0, 0.0}};
};

Mat3 mat_mul(const Mat3& a, const Mat3& b);
Mat3 mat_inverse(const Mat3& m);
Rgb mat_apply(const Mat3& m, const Rgb& v);

// Parsed .cube 3D LUT. Grid data is stored blue-fastest, matching the
// .cube row order: index(r, g, b) = ((r * size) + g) * size + b,
// three floats per node.
struct Lut3D {
  static constexpr int kMinSize = 2;
  static constexpr int kMaxSize = 65;

  int size = 0;  // valid range: kMinSize..kMaxSize
  std::string title;
  double domain_min[3] = {0.0, 0.0, 0.0};
  double domain_max[3] = {1.0, 1.0, 1.0};
  std::vector<float> data;  // size^3 * 3 entries

  std::size_t node_index(int ri, int gi, int bi) const {
    return ((static_cast<std::size_t>(ri) * static_cast<std::size_t>(size) +
             static_cast<std::size_t>(gi)) *
                static_cast<std::size_t>(size) +
            static_cast<std::size_t>(bi)) *
           3u;
  }
};

// Trilinear sample with domain mapping and out-of-domain clamping.
Rgb lut_sample(const Lut3D& lut, const Rgb& in);

// ---- color spaces ----
enum class ColorSpace { kLinearSrgb, kSrgb, kDisplayP3 };

bool parse_color_space(const std::string& text, ColorSpace& out);
const char* color_space_name(ColorSpace space);

// RGB <-> XYZ (D65) matrices for the space's primaries.
Mat3 rgb_to_xyz_matrix(ColorSpace space);
Mat3 xyz_to_rgb_matrix(ColorSpace space);

// Transfer-function decode/encode between encoded and linear-light values
// inside the space's own RGB coordinates. Negative inputs are mirrored.
Rgb space_decode(ColorSpace space, const Rgb& v);
Rgb space_encode(ColorSpace space, const Rgb& v);

// ---- rendering ----
struct RenderOptions {
  ColorSpace space = ColorSpace::kSrgb;
  bool clamp_output = true;
};

// Shared per-pixel grading kernel used by every render path: decode to
// linear light, apply the LUT, re-encode, optionally clamp to [0, 1].
Rgb grade_pixel(const Lut3D& lut, const Rgb& in, const RenderOptions& opt);

void render_scalar(const Lut3D& lut, const std::vector<Rgb>& input,
                   std::vector<Rgb>& output, const RenderOptions& opt);

// Tiled/batched path. block_size must be >= 1; throws std::invalid_argument
// otherwise. The final short block is fully covered.
void render_tiled(const Lut3D& lut, const std::vector<Rgb>& input,
                  std::vector<Rgb>& output, std::size_t block_size,
                  const RenderOptions& opt);

// ---- statistics ----
struct ChannelHistogram {
  std::vector<std::uint64_t> bins;  // [0, 1] split into equal bins, endpoints
                                    // inclusive
  std::uint64_t underflow = 0;      // values < 0, -inf and NaN
  std::uint64_t overflow = 0;       // values > 1 and +inf
};

struct HistogramSet {
  ChannelHistogram channel[3];  // r, g, b
  int bin_count = 0;
};

HistogramSet compute_histogram(const std::vector<Rgb>& pixels, int bin_count);

// Self-test entry point implemented in tests/self_test.cpp.
// Returns 0 on success and prints "self_test=passed".
int run_self_test();

}  // namespace lutcheck
