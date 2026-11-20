#ifndef GRADE_CUBE_LUT_HPP
#define GRADE_CUBE_LUT_HPP

#include <array>
#include <string>
#include <vector>

namespace grade {

class CubeParser; // defined in cube_lut.cpp, granted member access below

constexpr int kMinCubeGrid = 2;
constexpr int kMaxCubeGrid = 65;

// Parsed contents of an Adobe .cube 3D LUT. Data points follow the file
// layout: red varies fastest, then green, then blue; each entry is RGB.
class CubeLut {
public:
    using Rgb = std::array<double, 3>;

    int gridSize() const noexcept { return gridSize_; }
    const std::string& title() const noexcept { return title_; }
    const Rgb& domainMin() const noexcept { return domainMin_; }
    const Rgb& domainMax() const noexcept { return domainMax_; }
    const std::vector<Rgb>& data() const noexcept { return data_; }

    // Index into data() for grid coordinates (r, g, b), each in
    // [0, gridSize()-1]. Layout: r fastest, then g, then b.
    size_t index(int r, int g, int b) const noexcept;

    // Maps an encoded-space coordinate onto normalized [0,1] grid space
    // using the declared LUT domain.
    Rgb normalizeToDomain(const Rgb& value) const;

private:
    friend class CubeParser;
    friend CubeLut parseCubeString(const std::string& content);
    friend CubeLut parseCubeFile(const std::string& path);

    int gridSize_ = 0;
    std::string title_;
    Rgb domainMin_{{0.0, 0.0, 0.0}};
    Rgb domainMax_{{1.0, 1.0, 1.0}};
    std::vector<Rgb> data_;
};

// Reads and validates a .cube file. Format errors carry the offending
// 1-based line number; I/O failures throw IoError.
CubeLut parseCubeFile(const std::string& path);

// Parses from an in-memory string (used by tests and fixture generators).
CubeLut parseCubeString(const std::string& content);

} // namespace grade

#endif
