#ifndef GRADE_COLOR_HPP
#define GRADE_COLOR_HPP

#include <array>

#include "grade/cli.hpp"

namespace grade {

using Triple = std::array<double, 3>;
using Mat3 = std::array<std::array<double, 3>, 3>;

// --- transfer functions (encoded [0,1] <-> linear light) -----------------

// Both sRGB and Display-P3 use the IEC 61966-2-1 sRGB curve; the parameter
// keeps the call sites explicit about which space a value belongs to.
double decodeTransfer(ColorSpaceId space, double encoded);
double encodeTransfer(ColorSpaceId space, double linear);
Triple decodeTransfer(ColorSpaceId space, const Triple& encoded);
Triple encodeTransfer(ColorSpaceId space, const Triple& linear);

// --- matrices -------------------------------------------------------------

// D65-adapted linear-RGB -> XYZ matrices for the two supported spaces.
const Mat3& rgbToXyzMatrix(ColorSpaceId space);

// Inverts a 3x3 matrix; throws FormatError on non-finite entries or a
// (numerically) singular matrix.
Mat3 invertMatrix3(const Mat3& matrix);

Triple multiply(const Mat3& matrix, const Triple& vector);

// Linear RGB of one space -> linear RGB of the other, via XYZ (D65).
Triple convertLinearRgb(ColorSpaceId from, ColorSpaceId to,
                        const Triple& linear);

// Full encoded-to-encoded conversion: decode, chromatic adapt, encode.
Triple convertEncoded(ColorSpaceId from, ColorSpaceId to,
                      const Triple& encoded);

bool isFinite(const Triple& value);
bool isFinite(const Mat3& matrix);

} // namespace grade

#endif
