#pragma once

#include <array>
#include <string>

#include "types.hpp"

namespace lutcheck {

enum class ColorSpace { Rgb, Rec709, DisplayP3 };

const char* color_space_name(ColorSpace space);
bool parse_color_space(const std::string& text, ColorSpace* out);

// 3x3 matrix in row-major order.
struct Mat3 {
  std::array<double, 9> m{};

  static Mat3 identity();
  Vec3 apply(const Vec3& v) const;
  double determinant() const;
  Mat3 inverse() const;
};

Mat3 operator*(const Mat3& a, const Mat3& b);

// All matrices assume a D65 white point. `Rgb` is the default working space
// and is defined to share Rec.709 primaries (i.e. sRGB), so rgb <-> rec709 is
// the identity and every conversion is transitive.
Mat3 conversion_matrix(ColorSpace from, ColorSpace to);

// Builds the combined matrix used by the pipeline: input space -> LUT space.
// Throws an AppError with ExitCode::Usage for unknown spaces and
// ExitCode::Input when a matrix turns out to be singular.
Mat3 build_conversion(ColorSpace from, ColorSpace to);

// Enumerates the accepted space names for help text.
std::string known_color_spaces();

}  // namespace lutcheck
