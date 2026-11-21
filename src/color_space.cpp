#include "lut/color_space.hpp"

#include <cmath>

namespace lut {
namespace {

constexpr double kSingularEps = 1e-12;

double requireFinite(double v, const char* stage) {
    if (!std::isfinite(v)) {
        throw LutDataError(std::string("non-finite result in ") + stage);
    }
    return v;
}

// CIE xy chromaticity pair for a primary or white point.
struct Chroma {
    double x, y;
};

std::array<double, 3> xyzFromChroma(Chroma c) {
    return {c.x / c.y, 1.0, (1.0 - c.x - c.y) / c.y};
}

// Builds the D65 RGB->XYZ matrix from three primaries and the white point.
Mat3 buildRgbToXyz(const std::array<Chroma, 3>& primaries, Chroma white) {
    // Columns of P are the XYZ tristimuli of the three primaries.
    Mat3 p{};
    for (int col = 0; col < 3; ++col) {
        const auto v = xyzFromChroma(primaries[col]);
        p[0][col] = v[0];
        p[1][col] = v[1];
        p[2][col] = v[2];
    }
    const Mat3 pinv = p.inverse();
    const auto w = xyzFromChroma(white);
    const auto scale = multiply(pinv, w);  // Luminance weights S_r,S_g,S_b.

    Mat3 result{};
    for (int col = 0; col < 3; ++col) {
        result[0][col] = scale[col] * p[0][col];
        result[1][col] = scale[col] * p[1][col];
        result[2][col] = scale[col] * p[2][col];
    }
    for (int r = 0; r < 3; ++r) {
        for (int c = 0; c < 3; ++c) {
            requireFinite(result[r][c], "RGB/XYZ matrix build");
        }
    }
    return result;
}

double decodeSrgbLike(double v) {
    if (v <= 0.04045) {
        return v / 12.92;
    }
    return std::pow((v + 0.055) / 1.055, 2.4);
}

double encodeSrgbLike(double c) {
    if (c <= 0.0031308) {
        return 12.92 * c;
    }
    return 1.055 * std::pow(c, 1.0 / 2.4) - 0.055;
}

double decodeRec709(double v) {
    if (v < 0.081) {
        return v / 4.5;
    }
    return std::pow((v + 0.099) / 1.099, 1.0 / 0.45);
}

double encodeRec709(double c) {
    if (c < 0.018) {
        return 4.5 * c;
    }
    return 1.099 * std::pow(c, 0.45) - 0.099;
}

}  // namespace

Mat3 Mat3::identity() {
    Mat3 r{};
    r[0][0] = r[1][1] = r[2][2] = 1.0;
    return r;
}

double Mat3::determinant() const {
    return m[0][0] * (m[1][1] * m[2][2] - m[1][2] * m[2][1]) -
           m[0][1] * (m[1][0] * m[2][2] - m[1][2] * m[2][0]) +
           m[0][2] * (m[1][0] * m[2][1] - m[1][1] * m[2][0]);
}

Mat3 Mat3::inverse() const {
    const double det = determinant();
    if (std::fabs(det) < kSingularEps) {
        throw LutDataError("singular RGB/XYZ matrix");
    }
    Mat3 r{};
    const auto& a = m;
    // Cofactor matrix transposed (adjugate), divided by the determinant.
    r[0][0] = (a[1][1] * a[2][2] - a[1][2] * a[2][1]) / det;
    r[0][1] = (a[0][2] * a[2][1] - a[0][1] * a[2][2]) / det;
    r[0][2] = (a[0][1] * a[1][2] - a[0][2] * a[1][1]) / det;
    r[1][0] = (a[1][2] * a[2][0] - a[1][0] * a[2][2]) / det;
    r[1][1] = (a[0][0] * a[2][2] - a[0][2] * a[2][0]) / det;
    r[1][2] = (a[0][2] * a[1][0] - a[0][0] * a[1][2]) / det;
    r[2][0] = (a[1][0] * a[2][1] - a[1][1] * a[2][0]) / det;
    r[2][1] = (a[2][0] * a[0][1] - a[2][1] * a[0][0]) / det;
    r[2][2] = (a[0][0] * a[1][1] - a[0][1] * a[1][0]) / det;
    return r;
}

Mat3 multiply(const Mat3& a, const Mat3& b) {
    Mat3 r{};
    for (int i = 0; i < 3; ++i) {
        for (int j = 0; j < 3; ++j) {
            double sum = 0.0;
            for (int k = 0; k < 3; ++k) {
                sum += a[i][k] * b[k][j];
            }
            r[i][j] = sum;
        }
    }
    return r;
}

std::array<double, 3> multiply(const Mat3& a,
                               const std::array<double, 3>& v) {
    std::array<double, 3> r{};
    for (int i = 0; i < 3; ++i) {
        r[i] = a[i][0] * v[0] + a[i][1] * v[1] + a[i][2] * v[2];
    }
    return r;
}

ColorSpace::ColorSpace(ColorSpaceId id) : id_(id) {
    // D65 reference white shared by all three spaces.
    const Chroma white{0.3127, 0.3290};
    std::array<Chroma, 3> primaries{};
    if (id_ == ColorSpaceId::DisplayP3) {
        primaries = {Chroma{0.680, 0.320}, Chroma{0.265, 0.690},
                     Chroma{0.150, 0.060}};
    } else {
        // sRGB and Rec.709 share the BT.709 primaries.
        primaries = {Chroma{0.640, 0.330}, Chroma{0.300, 0.600},
                     Chroma{0.150, 0.060}};
    }
    toXyz_ = buildRgbToXyz(primaries, white);
    fromXyz_ = toXyz_.inverse();
}

double ColorSpace::decodeTransfer(double v) const {
    requireFinite(v, "transfer decode");
    if (id_ == ColorSpaceId::Rec709) {
        return requireFinite(decodeRec709(v), "Rec.709 decode");
    }
    return requireFinite(decodeSrgbLike(v), "sRGB/P3 decode");
}

double ColorSpace::encodeTransfer(double v) const {
    requireFinite(v, "transfer encode");
    if (id_ == ColorSpaceId::Rec709) {
        return requireFinite(encodeRec709(v), "Rec.709 encode");
    }
    return requireFinite(encodeSrgbLike(v), "sRGB/P3 encode");
}

RGB ColorSpace::decode(const RGB& encoded) const {
    return {decodeTransfer(encoded.r), decodeTransfer(encoded.g),
            decodeTransfer(encoded.b)};
}

RGB ColorSpace::encode(const RGB& linear) const {
    return {encodeTransfer(linear.r), encodeTransfer(linear.g),
            encodeTransfer(linear.b)};
}

RGB ColorSpace::convertLinear(const RGB& linear,
                              const ColorSpace& target) const {
    if (id_ == target.id_) {
        return linear;
    }
    const std::array<double, 3> in{linear.r, linear.g, linear.b};
    for (double v : in) {
        requireFinite(v, "linear conversion input");
    }
    const auto xyz = multiply(toXyz_, in);
    for (double v : xyz) {
        requireFinite(v, "XYZ intermediate");
    }
    const auto out = multiply(target.fromXyz_, xyz);
    RGB result{out[0], out[1], out[2]};
    requireFinite(result.r, "linear conversion output");
    requireFinite(result.g, "linear conversion output");
    requireFinite(result.b, "linear conversion output");
    return result;
}

}  // namespace lut
