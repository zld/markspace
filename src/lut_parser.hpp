#pragma once

#include <istream>
#include <string>
#include <vector>

#include "types.hpp"

namespace lutcheck {

inline constexpr int kMinGridSize = 2;
inline constexpr int kMaxGridSize = 65;

struct ParseLimits {
  int max_grid_size = kMaxGridSize;
};

// A parsed 3D LUT. `data` holds size^3 samples with the red axis varying
// fastest, which is the layout mandated by the .cube format:
//   index = r + g * size + b * size * size
struct Lut3D {
  std::string title;
  int size = 0;
  Vec3 domain_min{0.0, 0.0, 0.0};
  Vec3 domain_max{1.0, 1.0, 1.0};
  std::vector<Vec3> data;

  std::size_t node_count() const { return data.size(); }
  const Vec3& at(int r, int g, int b) const {
    return data[static_cast<std::size_t>(r) +
                static_cast<std::size_t>(g) * static_cast<std::size_t>(size) +
                static_cast<std::size_t>(b) * static_cast<std::size_t>(size) *
                    static_cast<std::size_t>(size)];
  }
};

// Parses a .cube stream. `source_name` is prefixed to every diagnostic so a
// failed batch item can be located without guesswork.
Lut3D parse_cube(std::istream& input, const std::string& source_name,
                 const ParseLimits& limits = {});

Lut3D load_cube_file(const std::string& path, const ParseLimits& limits = {});

}  // namespace lutcheck
