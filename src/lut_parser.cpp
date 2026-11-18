#include "lut_parser.hpp"

#include <cctype>
#include <cerrno>
#include <charconv>
#include <cmath>
#include <fstream>
#include <limits>
#include <sstream>
#include <string_view>
#include <utility>

#include "errors.hpp"

namespace lutcheck {
namespace {

std::string trim(std::string_view text) {
  std::size_t begin = 0;
  std::size_t end = text.size();
  auto is_space = [](char c) {
    return c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f';
  };
  while (begin < end && is_space(text[begin])) ++begin;
  while (end > begin && is_space(text[end - 1])) --end;
  return std::string(text.substr(begin, end - begin));
}

std::string where(const std::string& source, std::size_t line) {
  return source + ":" + std::to_string(line);
}

[[noreturn]] void bad_line(const std::string& source, std::size_t line,
                           const std::string& message) {
  fail(ExitCode::Input, where(source, line) + ": " + message);
}

double parse_number(const std::string& token, const std::string& source,
                    std::size_t line, const std::string& what) {
  if (token.empty()) {
    bad_line(source, line, "missing " + what);
  }
  double value = 0.0;
  const char* first = token.data();
  const char* last = token.data() + token.size();
  const auto result = std::from_chars(first, last, value);
  if (result.ec != std::errc() || result.ptr != last) {
    bad_line(source, line, "invalid " + what + " '" + token + "'");
  }
  if (!std::isfinite(value)) {
    bad_line(source, line, "non-finite " + what + " '" + token + "'");
  }
  return value;
}

std::vector<std::string> split_tokens(const std::string& line) {
  std::vector<std::string> tokens;
  std::size_t i = 0;
  while (i < line.size()) {
    while (i < line.size() && (line[i] == ' ' || line[i] == '\t')) ++i;
    if (i == line.size()) break;
    const std::size_t start = i;
    while (i < line.size() && line[i] != ' ' && line[i] != '\t') ++i;
    tokens.push_back(line.substr(start, i - start));
  }
  return tokens;
}

std::string parse_title(std::string_view rest) {
  std::string title = trim(rest);
  if (title.size() >= 2 && title.front() == '"' && title.back() == '"') {
    title = title.substr(1, title.size() - 2);
  }
  return title;
}

Vec3 parse_triplet(const std::vector<std::string>& tokens,
                   const std::string& source, std::size_t line,
                   const std::string& what, std::size_t offset) {
  if (tokens.size() != offset + 3) {
    bad_line(source, line,
             what + " requires exactly three values, got " +
                 std::to_string(tokens.size() >= offset
                                    ? tokens.size() - offset
                                    : 0));
  }
  return {parse_number(tokens[offset + 0], source, line, what + " value"),
          parse_number(tokens[offset + 1], source, line, what + " value"),
          parse_number(tokens[offset + 2], source, line, what + " value")};
}

}  // namespace

Lut3D parse_cube(std::istream& input, const std::string& source_name,
                 const ParseLimits& limits) {
  Lut3D lut;
  bool have_size = false;
  bool have_domain_min = false;
  bool have_domain_max = false;
  bool have_title = false;
  std::size_t domain_min_line = 0;
  std::size_t domain_max_line = 0;
  std::size_t line_number = 0;
  std::string raw;

  while (std::getline(input, raw)) {
    ++line_number;
    std::string line = trim(raw);
    if (line.empty() || line.front() == '#') continue;
    const auto tokens = split_tokens(line);
    const std::string& keyword = tokens.front();

    if (keyword == "TITLE") {
      if (have_title) bad_line(source_name, line_number, "duplicate TITLE declaration");
      have_title = true;
      lut.title = parse_title(line.substr(keyword.size()));
      continue;
    }
    if (keyword == "LUT_3D_SIZE") {
      if (have_size) {
        bad_line(source_name, line_number, "duplicate LUT_3D_SIZE declaration");
      }
      if (tokens.size() != 2) {
        bad_line(source_name, line_number, "LUT_3D_SIZE requires exactly one value");
      }
      const double raw_size =
          parse_number(tokens[1], source_name, line_number, "LUT_3D_SIZE value");
      if (raw_size != std::floor(raw_size)) {
        bad_line(source_name, line_number, "LUT_3D_SIZE must be an integer");
      }
      if (raw_size < static_cast<double>(kMinGridSize) ||
          raw_size > static_cast<double>(limits.max_grid_size)) {
        bad_line(source_name, line_number,
                 "LUT_3D_SIZE " + tokens[1] + " outside supported range [" +
                     std::to_string(kMinGridSize) + ", " +
                     std::to_string(limits.max_grid_size) + "]");
      }
      lut.size = static_cast<int>(raw_size);
      have_size = true;
      continue;
    }
    if (keyword == "DOMAIN_MIN" || keyword == "DOMAIN_MAX") {
      const bool is_min = keyword == "DOMAIN_MIN";
      bool& seen = is_min ? have_domain_min : have_domain_max;
      if (seen) {
        bad_line(source_name, line_number, "duplicate " + keyword + " declaration");
      }
      seen = true;
      Vec3 value = parse_triplet(tokens, source_name, line_number, keyword, 1);
      (is_min ? lut.domain_min : lut.domain_max) = value;
      (is_min ? domain_min_line : domain_max_line) = line_number;
      continue;
    }
    if (keyword == "LUT_1D_SIZE" || keyword == "LUT_1D_INPUT_RANGE" ||
        keyword == "LUT_3D_INPUT_RANGE") {
      bad_line(source_name, line_number, "unsupported directive '" + keyword + "'");
    }
    if (!have_size) {
      bad_line(source_name, line_number,
               "data row before LUT_3D_SIZE declaration");
    }
    const std::size_t expected =
        static_cast<std::size_t>(lut.size) * static_cast<std::size_t>(lut.size) *
        static_cast<std::size_t>(lut.size);
    if (lut.data.size() >= expected) {
      bad_line(source_name, line_number,
               "too many samples: expected " + std::to_string(expected) +
                   " for grid size " + std::to_string(lut.size));
    }
    // A row that starts with a letter is a directive we do not implement; a
    // numeric row is a sample. This keeps the line number attached to the
    // offending token instead of reporting a generic row-count mismatch.
    if (!keyword.empty() && std::isalpha(static_cast<unsigned char>(keyword[0]))) {
      bad_line(source_name, line_number, "unexpected directive '" + keyword + "'");
    }
    Vec3 sample = parse_triplet(tokens, source_name, line_number, "sample", 0);
    lut.data.push_back(sample);
  }

  if (input.bad()) {
    fail(ExitCode::Input, source_name + ": read failure");
  }
  if (!have_size) {
    fail(ExitCode::Input, source_name + ": missing LUT_3D_SIZE declaration");
  }
  const std::size_t expected =
      static_cast<std::size_t>(lut.size) * static_cast<std::size_t>(lut.size) *
      static_cast<std::size_t>(lut.size);
  if (lut.data.size() != expected) {
    fail(ExitCode::Input,
         source_name + ": expected " + std::to_string(expected) +
             " samples for grid size " + std::to_string(lut.size) + ", found " +
             std::to_string(lut.data.size()));
  }
  if (have_domain_min != have_domain_max) {
    const std::size_t line = have_domain_min ? domain_min_line : domain_max_line;
    fail(ExitCode::Input,
         where(source_name, line) +
             ": DOMAIN_MIN and DOMAIN_MAX must be declared together");
  }
  for (std::size_t channel = 0; channel < 3; ++channel) {
    const double low = lut.domain_min[channel];
    const double high = lut.domain_max[channel];
    if (!(high > low)) {
      fail(ExitCode::Input,
           where(source_name, domain_max_line) +
               ": inverted DOMAIN_MIN/DOMAIN_MAX on channel " +
               std::string(channel == 0 ? "R" : channel == 1 ? "G" : "B"));
    }
  }
  return lut;
}

Lut3D load_cube_file(const std::string& path, const ParseLimits& limits) {
  std::ifstream stream(path, std::ios::binary);
  if (!stream) {
    fail(ExitCode::Input, path + ": cannot open LUT file");
  }
  return parse_cube(stream, path, limits);
}

}  // namespace lutcheck
