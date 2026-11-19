#include "report.hpp"

#include <algorithm>
#include <charconv>
#include <cmath>
#include <sstream>
#include <utility>

#include "color_space.hpp"
#include "errors.hpp"
#include "histogram.hpp"
#include "interpolation.hpp"
#include "render_paths.hpp"

namespace lutcheck {
namespace {

std::string number(double value) {
  if (!std::isfinite(value)) {
    fail(ExitCode::Output, "refusing to serialize a non-finite value");
  }
  char buffer[64];
  const auto result = std::to_chars(buffer, buffer + sizeof(buffer), value,
                                    std::chars_format::general);
  if (result.ec != std::errc()) {
    fail(ExitCode::Output, "cannot format numeric value");
  }
  return std::string(buffer, result.ptr);
}

std::string string_array(const std::vector<std::string>& values) {
  std::string out = "[";
  for (std::size_t i = 0; i < values.size(); ++i) {
    if (i != 0) out += ',';
    out += json_quote(values[i]);
  }
  out += ']';
  return out;
}

}  // namespace

std::string json_quote(const std::string& text) {
  std::string out = "\"";
  for (char c : text) {
    switch (c) {
      case '"':
        out += "\\\"";
        break;
      case '\\':
        out += "\\\\";
        break;
      case '\n':
        out += "\\n";
        break;
      default:
        out.push_back(c);
    }
  }
  out.push_back('"');
  return out;
}

Analysis run_analysis(const Image& input, const Lut3D& lut,
                      const Options& options) {
  Analysis analysis;
  analysis.pixels = input.pixel_count();
  analysis.grid_size = lut.size;
  analysis.input_space = options.input_space;
  analysis.lut_space = options.lut_space;
  analysis.histogram_bins = options.histogram_bins;
  analysis.threshold = options.threshold;
  analysis.threads = options.threads;
  analysis.before = compute_histogram(input, options.histogram_bins);

  ColorSpace from = ColorSpace::Rgb;
  ColorSpace to = ColorSpace::Rgb;
  if (!parse_color_space(options.input_space, &from)) {
    fail(ExitCode::Usage, "unknown input color space '" + options.input_space +
                              "'; expected " + known_color_spaces());
  }
  if (!parse_color_space(options.lut_space, &to)) {
    fail(ExitCode::Usage, "unknown LUT color space '" + options.lut_space +
                              "'; expected " + known_color_spaces());
  }
  RenderOptions render;
  render.input_to_lut = build_conversion(from, to);

  const bool want_scalar = options.path_scalar;
  const bool want_parallel = options.path_parallel;
  if (!want_scalar && !want_parallel) {
    fail(ExitCode::Usage, "at least one render path must be selected");
  }

  Image reference;
  if (want_scalar) {
    reference = render_scalar(input, lut, render);
    analysis.paths.push_back("scalar");
  }
  if (want_parallel) {
    ParallelResult parallel =
        render_parallel(input, lut, render, options.threads);
    analysis.workers = parallel.threads_used;
    analysis.paths.push_back("parallel");
    if (want_scalar) {
      analysis.deviation = compare_images(reference, parallel.image);
      analysis.deviation_exceeded = analysis.deviation.max() > options.threshold;
    } else {
      reference = std::move(parallel.image);
    }
  }
  analysis.after = compute_histogram(reference, options.histogram_bins);
  analysis.rendered = std::move(reference);
  return analysis;
}

std::string analysis_json(const Analysis& analysis, bool pretty) {
  const std::string sep = pretty ? " " : "";
  const std::string newline = pretty ? "\n" : "";
  std::ostringstream out;
  out << '{' << newline;
  auto field = [&](const std::string& name, const std::string& value,
                   bool last) {
    out << sep << json_quote(name) << ':' << sep << value
        << (last ? "" : ",") << newline;
  };
  field("conversion", json_quote(analysis.input_space + "->" + analysis.lut_space),
        false);
  field("grid_size", std::to_string(analysis.grid_size), false);
  field("pixels", std::to_string(analysis.pixels), false);
  field("input_space", json_quote(analysis.input_space), false);
  field("lut_space", json_quote(analysis.lut_space), false);
  field("paths", string_array(analysis.paths), false);
  field("threads", std::to_string(analysis.threads), false);
  field("workers", std::to_string(analysis.workers), false);
  field("histogram_bins", std::to_string(analysis.histogram_bins), false);
  const std::string deviation =
      "{\"r\":" + number(analysis.deviation.r) +
      ",\"g\":" + number(analysis.deviation.g) +
      ",\"b\":" + number(analysis.deviation.b) + '}';
  field("max_deviation_rgb", deviation, false);
  field("deviation_threshold", number(analysis.threshold), false);
  field("deviation_exceeded", analysis.deviation_exceeded ? "true" : "false",
        false);
  field("histogram_before", histogram_json(analysis.before), false);
  field("histogram_after", histogram_json(analysis.after), true);
  out << '}';
  return out.str();
}

int report_exit_code(const Analysis& analysis) {
  return analysis.deviation_exceeded ? static_cast<int>(ExitCode::Deviation)
                                     : static_cast<int>(ExitCode::Ok);
}

std::string analysis_text(const Analysis& analysis) {
  std::ostringstream out;
  out << "pixels              : " << analysis.pixels << '\n';
  out << "grid size           : " << analysis.grid_size << '\n';
  out << "color conversion    : " << analysis.input_space << " -> "
      << analysis.lut_space << '\n';
  out << "paths               : " << string_array(analysis.paths) << '\n';
  out << "threads requested   : " << analysis.threads << '\n';
  out << "workers started     : " << analysis.workers << '\n';
  out << "max deviation rgb   : " << number(analysis.deviation.r) << ' '
      << number(analysis.deviation.g) << ' ' << number(analysis.deviation.b)
      << '\n';
  out << "deviation threshold : " << number(analysis.threshold) << '\n';
  out << "deviation exceeded  : "
      << (analysis.deviation_exceeded ? "yes" : "no") << '\n';
  return out.str();
}

}  // namespace lutcheck
