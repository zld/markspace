#ifndef LUT_MODEL_HPP
#define LUT_MODEL_HPP

#include <array>
#include <cstddef>
#include <stdexcept>
#include <string>
#include <string_view>
#include <vector>

namespace lut {

// Colour spaces supported for LUT bake/render operations.
enum class ColorSpaceId {
    Srgb,
    DisplayP3,
    Rec709,
};

// Behaviour when a sample falls outside the LUT domain.
enum class DomainPolicy {
    Clamp,
    Error,
};

// A single RGB triple. Values are linear-light or encoded depending on stage.
struct RGB {
    double r = 0.0;
    double g = 0.0;
    double b = 0.0;
};

// A uniform cubic 3D LUT. Nodes are stored red-fast: index = b*N*N + g*N + r.
struct Lut3D {
    int size = 0;
    std::array<double, 3> domainMin{0.0, 0.0, 0.0};
    std::array<double, 3> domainMax{1.0, 1.0, 1.0};
    std::vector<RGB> nodes;

    std::size_t index(int r, int g, int b) const {
        return static_cast<std::size_t>(b) * size * size +
               static_cast<std::size_t>(g) * size +
               static_cast<std::size_t>(r);
    }

    const RGB& at(int r, int g, int b) const { return nodes[index(r, g, b)]; }
    RGB& at(int r, int g, int b) { return nodes[index(r, g, b)]; }

    std::size_t nodeCount() const {
        return static_cast<std::size_t>(size) * size * size;
    }
};

// Base type for every structured failure raised by the pipeline.
class LutException : public std::runtime_error {
public:
    explicit LutException(const std::string& msg) : std::runtime_error(msg) {}
};

// Bad CLI arguments (exit code 2).
class UsageError : public LutException {
public:
    explicit UsageError(const std::string& msg) : LutException(msg) {}
};

// Malformed LUT data or invalid colour-space math (exit code 3).
class LutDataError : public LutException {
public:
    explicit LutDataError(const std::string& msg) : LutException(msg) {}
};

// File read/write and image format failures (exit code 4).
class IoError : public LutException {
public:
    explicit IoError(const std::string& msg) : LutException(msg) {}
};

// Thrown by the reader when a source line must be reported to the user.
class ParseError : public LutDataError {
public:
    ParseError(int line, const std::string& msg)
        : LutDataError("line " + std::to_string(line) + ": " + msg),
          line_(line) {}

    int line() const noexcept { return line_; }

private:
    int line_;
};

inline const char* name(ColorSpaceId id) noexcept {
    switch (id) {
        case ColorSpaceId::Srgb:
            return "srgb";
        case ColorSpaceId::DisplayP3:
            return "display-p3";
        case ColorSpaceId::Rec709:
            return "rec709";
    }
    return "unknown";
}

// Parses "srgb", "display-p3", "rec709" (case insensitive). Throws UsageError.
ColorSpaceId parseColorSpace(std::string_view text);

inline const char* name(DomainPolicy policy) noexcept {
    return policy == DomainPolicy::Clamp ? "clamp" : "error";
}

// Parses "clamp" or "error". Throws UsageError.
DomainPolicy parseDomainPolicy(std::string_view text);

}  // namespace lut

#endif  // LUT_MODEL_HPP
