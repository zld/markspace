#include "grade/color.hpp"

#include <cmath>

#include "grade/error.hpp"

namespace grade {

namespace {

constexpr double kTransferCutoff = 0.04045;
constexpr double kLinearSlope = 12.92;
constexpr double kGamma = 2.4;
constexpr double kAlpha = 1.055;
constexpr double kOffset = 0.055;
// Below this normalized determinant a matrix is treated as singular.
constexpr double kSingularEpsilon = 1.0e-10;

// sRGB (IEC 61966-2-1), D65.
const Mat3 kSrgbToXyz{{
    {{0.4123907992659595, 0.3575843393838780, 0.1804807884018343}},
    {{0.2126390058715103, 0.7151686787677560, 0.0721923153607337}},
    {{0.0193308187155918, 0.1191947797946259, 0.9505321522496607}},
}};

// Display P3 (SMPTE RP 431 primaries, D65 white point, sRGB curve).
const Mat3 kDisplayP3ToXyz{{
    {{0.4865709486482162, 0.2656676931690931, 0.1982172852343070}},
    {{0.2289745640697488, 0.6917385218365064, 0.0792869140937450}},
    {{0.0000000000000000, 0.0451133818589026, 1.0439443689009755}},
}};

double decodeCurve(double encoded) {
    if (encoded <= kTransferCutoff) {
        return encoded / kLinearSlope;
    }
    return std::pow((encoded + kOffset) / kAlpha, kGamma);
}

double encodeCurve(double linear) {
    if (linear <= kTransferCutoff / kLinearSlope) {
        return kLinearSlope * linear;
    }
    return kAlpha * std::pow(linear, 1.0 / kGamma) - kOffset;
}

} // namespace

double decodeTransfer(ColorSpaceId, double encoded) {
    return decodeCurve(encoded);
}

double encodeTransfer(ColorSpaceId, double linear) {
    return encodeCurve(linear);
}

Triple decodeTransfer(ColorSpaceId space, const Triple& encoded) {
    return {decodeTransfer(space, encoded[0]),
            decodeTransfer(space, encoded[1]),
            decodeTransfer(space, encoded[2])};
}

Triple encodeTransfer(ColorSpaceId space, const Triple& linear) {
    return {encodeTransfer(space, linear[0]),
            encodeTransfer(space, linear[1]),
            encodeTransfer(space, linear[2])};
}

const Mat3& rgbToXyzMatrix(ColorSpaceId space) {
    return space == ColorSpaceId::srgb ? kSrgbToXyz : kDisplayP3ToXyz;
}

bool isFinite(const Triple& value) {
    return std::isfinite(value[0]) && std::isfinite(value[1]) &&
           std::isfinite(value[2]);
}

bool isFinite(const Mat3& matrix) {
    for (const auto& row : matrix) {
        for (double cell : row) {
            if (!std::isfinite(cell)) {
                return false;
            }
        }
    }
    return true;
}

Mat3 invertMatrix3(const Mat3& m) {
    if (!isFinite(m)) {
        throw FormatError("color matrix contains non-finite entries");
    }
    // Cofactors of the 3x3 matrix.
    double c00 = m[1][1] * m[2][2] - m[1][2] * m[2][1];
    double c01 = m[1][2] * m[2][0] - m[1][0] * m[2][2];
    double c02 = m[1][0] * m[2][1] - m[1][1] * m[2][0];
    double c10 = m[0][2] * m[2][1] - m[0][1] * m[2][2];
    double c11 = m[0][0] * m[2][2] - m[0][2] * m[2][0];
    double c12 = m[0][1] * m[2][0] - m[0][0] * m[2][1];
    double c20 = m[0][1] * m[1][2] - m[0][2] * m[1][1];
    double c21 = m[0][2] * m[1][0] - m[0][0] * m[1][2];
    double c22 = m[0][0] * m[1][1] - m[0][1] * m[1][0];

    double determinant =
        m[0][0] * c00 + m[0][1] * c01 + m[0][2] * c02;
    if (!std::isfinite(determinant) ||
        std::fabs(determinant) < kSingularEpsilon) {
        throw FormatError("color matrix is singular and cannot be inverted");
    }
    double inv = 1.0 / determinant;
    // Adjugate (transpose of cofactor matrix) divided by the determinant.
    return Mat3{{
        {{c00 * inv, c10 * inv, c20 * inv}},
        {{c01 * inv, c11 * inv, c21 * inv}},
        {{c02 * inv, c12 * inv, c22 * inv}},
    }};
}

Triple multiply(const Mat3& m, const Triple& v) {
    Triple result{};
    for (size_t row = 0; row < 3; ++row) {
        result[row] = m[row][0] * v[0] + m[row][1] * v[1] +
                      m[row][2] * v[2];
    }
    return result;
}

Triple convertLinearRgb(ColorSpaceId from, ColorSpaceId to,
                        const Triple& linear) {
    if (from == to) {
        return linear;
    }
    const Mat3& forward = rgbToXyzMatrix(from);
    const Mat3 inverse = invertMatrix3(rgbToXyzMatrix(to));
    return multiply(inverse, multiply(forward, linear));
}

Triple convertEncoded(ColorSpaceId from, ColorSpaceId to,
                      const Triple& encoded) {
    Triple linear = decodeTransfer(from, encoded);
    Triple adapted = convertLinearRgb(from, to, linear);
    return encodeTransfer(to, adapted);
}

} // namespace grade
