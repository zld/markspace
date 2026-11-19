#include "color/types.hpp"

#include <algorithm>
#include <cctype>
#include <cmath>

namespace color {

namespace {

// IEC 61966-2-1 sRGB primaries and D65 white point.
constexpr double kSrgbToXyz[9] = {
    0.412390799265959, 0.357584339383878, 0.180480788401834,
    0.212639005871510, 0.715168678767756, 0.0721923153607337,
    0.0193308187155918, 0.119194779794626, 0.950532152249661,
};

constexpr double kXyzToSrgb[9] = {
    3.24096994190452, -1.53738317757009, -0.498610760293,
    -0.96924363628088, 1.87596750150772, 0.0415550574071756,
    0.0556300796969936, -0.203976958888976, 1.05697151424288,
};

Mat3 makeMatrix(const double (&values)[9]) {
    Mat3 out;
    std::copy(std::begin(values), std::end(values), out.e.begin());
    return out;
}

void requireFiniteResult(const RGB& value, const char* stage) {
    if (!isFinite(value)) {
        throw ColorError(std::string("colour conversion produced non-finite values in ") +
                         stage);
    }
}

}  // namespace

ColorSpace parseColorSpace(const std::string& name) {
    std::string normalized;
    normalized.reserve(name.size());
    for (char ch : name) {
        const unsigned char uch = static_cast<unsigned char>(ch);
        if (std::isalnum(uch)) {
            normalized.push_back(static_cast<char>(std::tolower(uch)));
        } else if (ch == '-' || ch == '_' || ch == ' ') {
            normalized.push_back('-');
        } else {
            throw ColorError("unrecognised colour space: " + name);
        }
    }
    if (normalized == "srgb") return ColorSpace::Srgb;
    if (normalized == "linear-rgb" || normalized == "linearrgb" || normalized == "linear") {
        return ColorSpace::LinearRgb;
    }
    if (normalized == "xyz") return ColorSpace::Xyz;
    throw ColorError("unrecognised colour space: " + name);
}

const char* colorSpaceName(ColorSpace space) {
    switch (space) {
        case ColorSpace::Srgb: return "srgb";
        case ColorSpace::LinearRgb: return "linear-rgb";
        case ColorSpace::Xyz: return "xyz";
    }
    return "unknown";
}

RGB srgbToLinear(const RGB& encoded) {
    auto channel = [](double c) {
        return c <= 0.04045 ? c / 12.92 : std::pow((c + 0.055) / 1.055, 2.4);
    };
    return {channel(encoded.r), channel(encoded.g), channel(encoded.b)};
}

RGB linearToSrgb(const RGB& linear) {
    auto channel = [](double c) {
        return c <= 0.0031308 ? 12.92 * c : 1.055 * std::pow(c, 1.0 / 2.4) - 0.055;
    };
    return {channel(linear.r), channel(linear.g), channel(linear.b)};
}

const Mat3& srgbToXyzMatrix() {
    static const Mat3 matrix = makeMatrix(kSrgbToXyz);
    return matrix;
}

const Mat3& xyzToSrgbMatrix() {
    static const Mat3 matrix = makeMatrix(kXyzToSrgb);
    return matrix;
}

RGB linearRgbToXyz(const RGB& linear) { return multiply(srgbToXyzMatrix(), linear); }

RGB xyzToLinearRgb(const RGB& xyz) {
    // Guard the published inverse against drift: the CLI can only locate bad
    // matrices if the inverse actually is invertible.
    static const bool checked = [] {
        Mat3 computed;
        if (!invertMatrix(srgbToXyzMatrix(), computed)) {
            throw ColorError("sRGB to XYZ matrix is singular");
        }
        for (std::size_t i = 0; i < computed.e.size(); ++i) {
            if (std::fabs(computed.e[i] - xyzToSrgbMatrix().e[i]) > 1e-9) {
                throw ColorError("XYZ to sRGB matrix does not invert sRGB to XYZ matrix");
            }
        }
        return true;
    }();
    (void)checked;
    return multiply(xyzToSrgbMatrix(), xyz);
}

RGB convertColor(ColorSpace from, ColorSpace to, const RGB& value, std::size_t pixelIndex) {
    if (!isFinite(value)) {
        std::string where = "input pixel";
        if (pixelIndex != kNoPixel) where += " " + std::to_string(pixelIndex);
        throw ColorError(where + " has a non-finite component");
    }
    if (from == to) return value;

    RGB result = value;
    if (from == ColorSpace::Srgb) result = srgbToLinear(result);
    if (from == ColorSpace::Xyz) result = xyzToLinearRgb(result);

    if (to == ColorSpace::Xyz) {
        result = linearRgbToXyz(result);
    } else if (to == ColorSpace::Srgb) {
        result = linearToSrgb(result);
    }
    requireFiniteResult(result, "convertColor");
    return result;
}

}  // namespace color
