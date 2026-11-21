#ifndef LUT_COLOR_SPACE_HPP
#define LUT_COLOR_SPACE_HPP

#include <array>

#include "lut/model.hpp"

namespace lut {

// Row-major 3x3 matrix for RGB <-> CIE XYZ conversions at D65.
struct Mat3 {
    std::array<std::array<double, 3>, 3> m{};

    static Mat3 identity();

    const std::array<double, 3>& operator[](int row) const { return m[row]; }
    std::array<double, 3>& operator[](int row) { return m[row]; }

    double determinant() const;
    Mat3 inverse() const;  // Throws LutDataError when singular.
};

Mat3 multiply(const Mat3& a, const Mat3& b);
std::array<double, 3> multiply(const Mat3& a, const std::array<double, 3>& v);

// A colour space: transfer function plus D65 RGB/XYZ primary matrices.
class ColorSpace {
public:
    explicit ColorSpace(ColorSpaceId id);

    ColorSpaceId id() const noexcept { return id_; }
    const Mat3& rgbToXyz() const noexcept { return toXyz_; }
    const Mat3& xyzToRgb() const noexcept { return fromXyz_; }

    // Encoded signal -> linear light. Finite input, domain enforced by caller.
    double decodeTransfer(double v) const;
    // Linear light -> encoded signal.
    double encodeTransfer(double v) const;

    RGB decode(const RGB& encoded) const;
    RGB encode(const RGB& linear) const;

    // Linear RGB in this space to linear RGB in the target space.
    RGB convertLinear(const RGB& linear, const ColorSpace& target) const;

private:
    ColorSpaceId id_;
    Mat3 toXyz_;
    Mat3 fromXyz_;
};

}  // namespace lut

#endif  // LUT_COLOR_SPACE_HPP
