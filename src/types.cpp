#include "color/types.hpp"

#include <cmath>

namespace color {

namespace {
[[noreturn]] void throwIndex(int channel) {
    throw std::out_of_range("RGB channel index out of range: " + std::to_string(channel));
}
}  // namespace

double& RGB::operator[](int channel) {
    switch (channel) {
        case 0: return r;
        case 1: return g;
        case 2: return b;
        default: throwIndex(channel);
    }
}

const double& RGB::operator[](int channel) const {
    switch (channel) {
        case 0: return r;
        case 1: return g;
        case 2: return b;
        default: throwIndex(channel);
    }
}

bool operator==(const RGB& lhs, const RGB& rhs) {
    return lhs.r == rhs.r && lhs.g == rhs.g && lhs.b == rhs.b;
}

bool isFinite(const RGB& value) {
    return std::isfinite(value.r) && std::isfinite(value.g) && std::isfinite(value.b);
}

RGB componentMin(const RGB& lhs, const RGB& rhs) {
    return {std::fmin(lhs.r, rhs.r), std::fmin(lhs.g, rhs.g), std::fmin(lhs.b, rhs.b)};
}

RGB componentMax(const RGB& lhs, const RGB& rhs) {
    return {std::fmax(lhs.r, rhs.r), std::fmax(lhs.g, rhs.g), std::fmax(lhs.b, rhs.b)};
}

double& Mat3::operator()(int row, int column) {
    if (row < 0 || row > 2 || column < 0 || column > 2) {
        throw std::out_of_range("matrix index out of range");
    }
    return e[static_cast<std::size_t>(row * 3 + column)];
}

const double& Mat3::operator()(int row, int column) const {
    if (row < 0 || row > 2 || column < 0 || column > 2) {
        throw std::out_of_range("matrix index out of range");
    }
    return e[static_cast<std::size_t>(row * 3 + column)];
}

bool invertMatrix(const Mat3& matrix, Mat3& inverse, double epsilon) {
    for (double value : matrix.e) {
        if (!std::isfinite(value)) return false;
    }
    // Gauss-Jordan on [matrix | identity]. Partial pivoting keeps the result
    // stable for the sRGB matrices and rejects singular input.
    Mat3 work = matrix;
    Mat3 result;
    result.e = {1.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 1.0};
    for (int column = 0; column < 3; ++column) {
        int pivot = column;
        double best = std::fabs(work(column, column));
        for (int row = column + 1; row < 3; ++row) {
            const double candidate = std::fabs(work(row, column));
            if (candidate > best) {
                best = candidate;
                pivot = row;
            }
        }
        if (!(best > epsilon)) return false;
        if (pivot != column) {
            for (int k = 0; k < 3; ++k) {
                std::swap(work(column, k), work(pivot, k));
                std::swap(result(column, k), result(pivot, k));
            }
        }
        const double scale = work(column, column);
        for (int k = 0; k < 3; ++k) {
            work(column, k) /= scale;
            result(column, k) /= scale;
        }
        for (int row = 0; row < 3; ++row) {
            if (row == column) continue;
            const double factor = work(row, column);
            if (factor == 0.0) continue;
            for (int k = 0; k < 3; ++k) {
                work(row, k) -= factor * work(column, k);
                result(row, k) -= factor * result(column, k);
            }
        }
    }
    inverse = result;
    return true;
}

Mat3 multiply(const Mat3& lhs, const Mat3& rhs) {
    Mat3 out;
    for (int row = 0; row < 3; ++row) {
        for (int column = 0; column < 3; ++column) {
            double sum = 0.0;
            for (int k = 0; k < 3; ++k) sum += lhs(row, k) * rhs(k, column);
            out(row, column) = sum;
        }
    }
    return out;
}

RGB multiply(const Mat3& matrix, const RGB& value) {
    return {matrix(0, 0) * value.r + matrix(0, 1) * value.g + matrix(0, 2) * value.b,
            matrix(1, 0) * value.r + matrix(1, 1) * value.g + matrix(1, 2) * value.b,
            matrix(2, 0) * value.r + matrix(2, 1) * value.g + matrix(2, 2) * value.b};
}

RGB Domain::width() const {
    return {max.r - min.r, max.g - min.g, max.b - min.b};
}

bool Domain::valid() const {
    return isFinite(min) && isFinite(max) && width().r > 0.0 && width().g > 0.0 &&
           width().b > 0.0;
}

bool Domain::contains(const RGB& value) const {
    return isFinite(value) && value.r >= min.r && value.r <= max.r && value.g >= min.g &&
           value.g <= max.g && value.b >= min.b && value.b <= max.b;
}

std::size_t LUT::sampleCount() const {
    if (nx <= 0 || ny <= 0 || nz <= 0) return 0;
    return static_cast<std::size_t>(nx) * static_cast<std::size_t>(ny) *
           static_cast<std::size_t>(nz);
}

std::size_t LUT::index(int x, int y, int z) const {
    return (static_cast<std::size_t>(z) * static_cast<std::size_t>(ny) +
            static_cast<std::size_t>(y)) *
               static_cast<std::size_t>(nx) +
           static_cast<std::size_t>(x);
}

const RGB& LUT::at(int x, int y, int z) const { return data.at(index(x, y, z)); }

void validateLut(const LUT& lut) {
    if (lut.nx < kMinLutSize || lut.nx > kMaxLutSize || lut.ny < kMinLutSize ||
        lut.ny > kMaxLutSize || lut.nz < kMinLutSize || lut.nz > kMaxLutSize) {
        throw LutError("LUT grid size out of range [2,65]: " + std::to_string(lut.nx) + "x" +
                       std::to_string(lut.ny) + "x" + std::to_string(lut.nz));
    }
    if (!lut.domain.valid()) {
        throw LutError("LUT domain is invalid (zero or non-finite width)");
    }
    if (lut.data.size() != lut.sampleCount()) {
        throw LutError("LUT sample count mismatch: expected " +
                       std::to_string(lut.sampleCount()) + ", got " +
                       std::to_string(lut.data.size()));
    }
    for (std::size_t i = 0; i < lut.data.size(); ++i) {
        if (!isFinite(lut.data[i])) {
            throw LutError("LUT sample " + std::to_string(i) + " is not finite");
        }
    }
}

}  // namespace color
