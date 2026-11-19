// cube_parser.cpp - strict .cube 3D LUT parser with line-numbered errors.
#include "cube_parser.hpp"

#include <cmath>
#include <cstddef>
#include <cstdlib>
#include <fstream>
#include <sstream>
#include <string>

namespace lutcheck {

ParseError::ParseError(int line, const std::string& message)
    : std::runtime_error(line > 0
                             ? "line " + std::to_string(line) + ": " + message
                             : message),
      line_(line) {}

namespace {

// Parse a token as a finite double; rejects trailing garbage, NaN and inf.
bool parse_finite(const std::string& token, double& out) {
  if (token.empty()) return false;
  char* end = nullptr;
  const double value = std::strtod(token.c_str(), &end);
  if (end == token.c_str() || *end != '\0') return false;
  if (!std::isfinite(value)) return false;
  out = value;
  return true;
}

std::string trim(const std::string& s) {
  const std::size_t first = s.find_first_not_of(" \t\r");
  if (first == std::string::npos) return "";
  return s.substr(first, s.find_last_not_of(" \t\r") - first + 1);
}

struct Parser {
  bool has_title = false;
  bool has_size = false;
  bool has_domain_min = false;
  bool has_domain_max = false;
  int domain_min_line = 0;
  int domain_max_line = 0;
  int size = 0;
  std::size_t expected_nodes = 0;
  std::size_t data_rows = 0;
  Lut3D lut;
};

void check_duplicate(bool already, int line, const char* keyword) {
  if (already) {
    throw ParseError(line, std::string("duplicate ") + keyword + " declaration");
  }
}

void parse_data_row(Parser& p, int line, std::istringstream& tokens) {
  if (!p.has_size) {
    throw ParseError(line, "data row appears before LUT_3D_SIZE");
  }
  double rgb[3];
  std::string token;
  for (double& v : rgb) {
    if (!(tokens >> token) || !parse_finite(token, v)) {
      throw ParseError(line, "data row must have exactly 3 finite numbers");
    }
  }
  if (tokens >> token) {
    throw ParseError(line, "data row must have exactly 3 finite numbers");
  }
  if (p.data_rows >= p.expected_nodes) {
    throw ParseError(line, "more data rows than LUT_3D_SIZE^3");
  }
  for (double v : rgb) p.lut.data.push_back(static_cast<float>(v));
  ++p.data_rows;
}

}  // namespace

Lut3D parse_cube_text(const std::string& text) {
  Parser p;
  std::istringstream stream(text);
  std::string raw;
  int line = 0;
  while (std::getline(stream, raw)) {
    ++line;
    const std::size_t comment = raw.find('#');
    const std::string cleaned = trim(raw.substr(0, comment));
    if (cleaned.empty()) continue;

    std::istringstream tokens(cleaned);
    std::string keyword;
    tokens >> keyword;

    if (keyword == "TITLE") {
      check_duplicate(p.has_title, line, "TITLE");
      p.has_title = true;
      std::string rest;
      std::getline(tokens, rest);
      p.lut.title = trim(rest);
    } else if (keyword == "LUT_3D_SIZE") {
      check_duplicate(p.has_size, line, "LUT_3D_SIZE");
      std::string token, extra;
      double value = 0.0;
      if (!(tokens >> token) || !parse_finite(token, value) ||
          value != std::floor(value) || (tokens >> extra)) {
        throw ParseError(line, "LUT_3D_SIZE must be a single integer");
      }
      const int n = static_cast<int>(value);
      if (n < Lut3D::kMinSize || n > Lut3D::kMaxSize) {
        throw ParseError(line, "LUT_3D_SIZE out of range [2, 65]");
      }
      p.has_size = true;
      p.size = n;
      p.lut.size = n;
      // Overflow-safe node count: n <= 65 so n^3 fits easily, but verify.
      const std::size_t side = static_cast<std::size_t>(n);
      if (side != 0 && side > static_cast<std::size_t>(-1) / (side * side * 3u)) {
        throw ParseError(line, "LUT_3D_SIZE overflows node count");
      }
      p.expected_nodes = side * side * side;
      p.lut.data.reserve(p.expected_nodes * 3u);
    } else if (keyword == "LUT_1D_SIZE") {
      throw ParseError(line, "LUT_1D_SIZE is not supported (3D LUTs only)");
    } else if (keyword == "DOMAIN_MIN" || keyword == "DOMAIN_MAX") {
      const bool is_min = keyword == "DOMAIN_MIN";
      check_duplicate(is_min ? p.has_domain_min : p.has_domain_max, line,
                      keyword.c_str());
      double* target = is_min ? p.lut.domain_min : p.lut.domain_max;
      std::string token, extra;
      for (int c = 0; c < 3; ++c) {
        if (!(tokens >> token) || !parse_finite(token, target[c])) {
          throw ParseError(line, keyword + " must have exactly 3 finite numbers");
        }
      }
      if (tokens >> extra) {
        throw ParseError(line, keyword + " must have exactly 3 finite numbers");
      }
      if (is_min) {
        p.has_domain_min = true;
        p.domain_min_line = line;
      } else {
        p.has_domain_max = true;
        p.domain_max_line = line;
      }
    } else {
      double first = 0.0;
      if (!parse_finite(keyword, first)) {
        throw ParseError(line, "unrecognized keyword: " + keyword);
      }
      // Rewind and parse the whole row as data.
      std::istringstream row(cleaned);
      parse_data_row(p, line, row);
    }
  }

  if (!p.has_size) {
    throw ParseError(0, "missing LUT_3D_SIZE declaration");
  }
  for (int c = 0; c < 3; ++c) {
    if (p.lut.domain_min[c] >= p.lut.domain_max[c]) {
      const int report_line =
          p.has_domain_max ? p.domain_max_line : p.domain_min_line;
      throw ParseError(report_line,
                       "DOMAIN_MIN must be strictly below DOMAIN_MAX per "
                       "channel (equal or inverted range)");
    }
  }
  if (p.data_rows != p.expected_nodes) {
    throw ParseError(0, "expected " + std::to_string(p.expected_nodes) +
                            " data rows (LUT_3D_SIZE^3) but found " +
                            std::to_string(p.data_rows));
  }
  return p.lut;
}

Lut3D parse_cube_file(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  if (!in) {
    throw IoError("cannot open LUT file: " + path);
  }
  std::ostringstream buffer;
  buffer << in.rdbuf();
  if (in.bad()) {
    throw IoError("failed while reading LUT file: " + path);
  }
  return parse_cube_text(buffer.str());
}

}  // namespace lutcheck
