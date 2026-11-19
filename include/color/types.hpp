#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace color {

constexpr int kMinLutSize = 2;
constexpr int kMaxLutSize = 65;
constexpr std::size_t kMaxPixels = 16777216;
constexpr int kMaxThreads = 32;
constexpr std::size_t kHistogramBuckets = 16;

// Runtime failures are split into stable categories so the CLI can map them to
// exit codes: data errors (LUT / colour space) and processing errors.
class LutError : public std::runtime_error {
public:
    explicit LutError(const std::string& message) : std::runtime_error(message) {}
};

class ColorError : public LutError {
public:
    explicit ColorError(const std::string& message) : LutError(message) {}
};

class ProcessingError : public std::runtime_error {
public:
    explicit ProcessingError(const std::string& message) : std::runtime_error(message) {}
};

struct RGB {
    double r = 0.0;
    double g = 0.0;
    double b = 0.0;

    double& operator[](int channel);
    const double& operator[](int channel) const;
};

bool operator==(const RGB& lhs, const RGB& rhs);
bool isFinite(const RGB& value);
RGB componentMin(const RGB& lhs, const RGB& rhs);
RGB componentMax(const RGB& lhs, const RGB& rhs);

struct Mat3 {
    std::array<double, 9> e{};
    double& operator()(int row, int column);
    const double& operator()(int row, int column) const;
};

// Gauss-Jordan inversion with a determinant guard. Returns false for a singular
// or non-finite matrix; the output is untouched on failure.
bool invertMatrix(const Mat3& matrix, Mat3& inverse, double epsilon = 1e-12);
Mat3 multiply(const Mat3& lhs, const Mat3& rhs);
RGB multiply(const Mat3& matrix, const RGB& value);

// A LUT grid domain. width = max - min and must be strictly positive.
struct Domain {
    RGB min{0.0, 0.0, 0.0};
    RGB max{1.0, 1.0, 1.0};

    RGB width() const;
    bool valid() const;
    bool contains(const RGB& value) const;
};

}  // namespace color
