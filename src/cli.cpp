#include "cli.hpp"

#include <charconv>
#include <iostream>
#include <cmath>
#include <sstream>
#include <string>
#include <system_error>
#include <vector>

#include "color_space.hpp"
#include "errors.hpp"
#include "histogram.hpp"
#include "fixtures.hpp"
#include "lut_parser.hpp"
#include "ppm.hpp"
#include "render_paths.hpp"
#include "report.hpp"

namespace lutcheck {
namespace {

constexpr std::size_t kMaxBatchInputs = 4096;

bool parse_int(const std::string& text, int* out) {
  if (text.empty()) return false;
  int value = 0;
  const char* first = text.data();
  const char* last = text.data() + text.size();
  const auto result = std::from_chars(first, last, value);
  if (result.ec != std::errc() || result.ptr != last) return false;
  *out = value;
  return true;
}

bool parse_double(const std::string& text, double* out) {
  if (text.empty()) return false;
  double value = 0.0;
  const char* first = text.data();
  const char* last = text.data() + text.size();
  const auto result = std::from_chars(first, last, value);
  if (result.ec != std::errc() || result.ptr != last) return false;
  if (!std::isfinite(value)) return false;
  *out = value;
  return true;
}

std::vector<std::string> split_list(const std::string& text) {
  std::vector<std::string> parts;
  std::size_t start = 0;
  while (start <= text.size()) {
    const std::size_t comma = text.find(',', start);
    const std::size_t end = comma == std::string::npos ? text.size() : comma;
    if (end > start) parts.push_back(text.substr(start, end - start));
    if (comma == std::string::npos) break;
    start = comma + 1;
  }
  return parts;
}

bool fail_with(std::string* error, const std::string& message) {
  if (error != nullptr) *error = message;
  return false;
}

// Sentinel used by the value-taking flags: reports a missing trailing value
// for every flag uniformly.
struct ArgCursor {
  int argc;
  char** argv;
  int index;

  bool next(std::string* out) {
    if (index + 1 >= argc) return false;
    *out = argv[++index];
    return true;
  }
};

bool parse_compare(const std::string& value, Options* options,
                   std::string* error) {
  const auto parts = split_list(value);
  if (parts.empty()) {
    return fail_with(error, "--compare needs at least one path");
  }
  for (const std::string& part : parts) {
    if (part == "scalar") {
      options->path_scalar = true;
    } else if (part == "parallel") {
      options->path_parallel = true;
    } else {
      return fail_with(error,
                       "unknown render path '" + part +
                           "' (expected scalar or parallel)");
    }
  }
  return true;
}

bool validate_options(const Options& options, std::string* error) {
  if (options.command.empty() && !options.help) {
    return fail_with(error, "missing command");
  }
  if (options.grid_size < kMinGridSize || options.grid_size > kMaxGridSize) {
    return fail_with(error, "--size must be in [" +
                                std::to_string(kMinGridSize) + ", " +
                                std::to_string(kMaxGridSize) + "]");
  }
  if (options.threads < 1 || options.threads > kMaxThreads) {
    return fail_with(error, "--threads must be in [1, " +
                                std::to_string(kMaxThreads) + "]");
  }
  if (options.histogram_bins < kMinHistogramBins ||
      options.histogram_bins > kMaxHistogramBins) {
    return fail_with(error, "--histogram-bins must be in [" +
                                std::to_string(kMinHistogramBins) + ", " +
                                std::to_string(kMaxHistogramBins) + "]");
  }
  if (!std::isfinite(options.threshold) || options.threshold < 0.0) {
    return fail_with(error, "--threshold must be a finite value >= 0");
  }
  if (!parse_color_space(options.input_space, nullptr)) {
    return fail_with(error, "unknown input color space '" + options.input_space +
                                "'; expected " + known_color_spaces());
  }
  if (!parse_color_space(options.lut_space, nullptr)) {
    return fail_with(error, "unknown LUT color space '" + options.lut_space +
                                "'; expected " + known_color_spaces());
  }
  // Only rendering commands need a path; parse is pure inspection.
  const bool renders = options.command == "demo" ||
                       options.command == "render" ||
                       options.command == "batch";
  if (renders && !options.path_scalar && !options.path_parallel) {
    return fail_with(error, "no render path selected; use --compare");
  }
  if (!options.output.empty() && options.command == "parse") {
    return fail_with(error, "parse does not write rendered images");
  }
  if (options.command == "demo") {
    if (options.generate_fixtures && options.output.empty()) {
      // Defaults are filled by run_demo; nothing to reject.
    }
  } else if (options.command == "render") {
    if (options.input.empty()) {
      return fail_with(error, "render requires --input");
    }
    if (options.lut.empty()) {
      return fail_with(error, "render requires --lut");
    }
  } else if (options.command == "parse") {
    if (options.lut.empty()) {
      return fail_with(error, "parse requires --lut");
    }
  } else if (options.command == "batch") {
    if (options.inputs.empty()) {
      return fail_with(error, "batch requires --inputs");
    }
    if (options.out_dir.empty()) {
      return fail_with(error, "batch requires --out-dir");
    }
    if (options.inputs.size() > kMaxBatchInputs) {
      return fail_with(error, "batch input count exceeds " +
                                  std::to_string(kMaxBatchInputs));
    }
    if (options.lut.empty()) {
      return fail_with(error, "batch requires --lut");
    }
  } else {
    return fail_with(error, "unknown command '" + options.command + "'");
  }
  return true;
}

}  // namespace

bool parse_options(int argc, char** argv, Options* options, std::string* error) {
  if (argc <= 1) {
    return fail_with(error, "missing command");
  }
  options->command = argv[1];
  if (options->command == "--help" || options->command == "-h") {
    options->command.clear();
    options->help = true;
    return true;
  }
  ArgCursor cursor{argc, argv, 1};
  for (int i = 2; i < argc; ++i) {
    cursor.index = i;
    std::string flag = argv[i];
    std::string value;
    const auto need = [&]() {
      if (!cursor.next(&value)) {
        return fail_with(error, flag + " requires a value");
      }
      return true;
    };

    if (flag == "--help" || flag == "-h") {
      options->help = true;
    } else if (flag == "--json") {
      options->json = true;
    } else if (flag == "--generate-fixtures") {
      options->generate_fixtures = true;
    } else if (flag == "--input") {
      if (!need()) return false;
      options->input = value;
    } else if (flag == "--inputs") {
      if (!need()) return false;
      options->inputs = split_list(value);
      if (options->inputs.empty()) {
        return fail_with(error, "--inputs requires at least one path");
      }
    } else if (flag == "--lut") {
      if (!need()) return false;
      options->lut = value;
    } else if (flag == "--output") {
      if (!need()) return false;
      options->output = value;
      options->write_rendered = true;
    } else if (flag == "--out-dir") {
      if (!need()) return false;
      options->out_dir = value;
    } else if (flag == "--space") {
      if (!need()) return false;
      options->lut_space = value;
      options->input_space = value;
    } else if (flag == "--input-space") {
      if (!need()) return false;
      options->input_space = value;
    } else if (flag == "--lut-space") {
      if (!need()) return false;
      options->lut_space = value;
    } else if (flag == "--size") {
      if (!need()) return false;
      if (!parse_int(value, &options->grid_size)) {
        return fail_with(error, "--size expects an integer, got '" + value + "'");
      }
    } else if (flag == "--threads") {
      if (!need()) return false;
      if (!parse_int(value, &options->threads)) {
        return fail_with(error,
                         "--threads expects an integer, got '" + value + "'");
      }
    } else if (flag == "--histogram-bins") {
      if (!need()) return false;
      if (!parse_int(value, &options->histogram_bins)) {
        return fail_with(error, "--histogram-bins expects an integer, got '" +
                                    value + "'");
      }
    } else if (flag == "--threshold") {
      if (!need()) return false;
      if (!parse_double(value, &options->threshold)) {
        return fail_with(error, "--threshold expects a number, got '" + value +
                                    "'");
      }
    } else if (flag == "--compare") {
      if (!need()) return false;
      if (!parse_compare(value, options, error)) return false;
    } else {
      return fail_with(error, "unknown option '" + flag + "'");
    }
    i = cursor.index;
  }
  if (options->help) return true;
  return validate_options(*options, error);
}

std::string usage_text() {
  std::ostringstream out;
  out << "lutcheck - local 3D LUT validation and conversion toolkit\n\n";
  out << "Usage:\n";
  out << "  lutcheck demo   [--generate-fixtures] [--size N] [--space S]\n";
  out << "                  [--compare scalar,parallel] [--histogram-bins N]\n";
  out << "                  [--threads N] [--threshold T] [--json]\n";
  out << "  lutcheck render --input IMAGE.ppm [--lut LUT.cube] [--output OUT.ppm]\n";
  out << "                  [--input-space S] [--lut-space S]\n";
  out << "                  [--compare scalar,parallel] [--threads N]\n";
  out << "                  [--histogram-bins N] [--threshold T] [--json]\n";
  out << "  lutcheck batch  --inputs A.ppm,B.ppm --out-dir DIR --lut LUT.cube\n";
  out << "                  [--input-space S] [--lut-space S] [--threads N]\n";
  out << "                  [--histogram-bins N] [--json]\n";
  out << "  lutcheck parse  --lut LUT.cube [--json]\n\n";
  out << "Color spaces: " << known_color_spaces() << "\n";
  out << "Limits: grid size [" << kMinGridSize << ", " << kMaxGridSize
      << "], pixels <= " << kMaxPixels << ", threads [1, " << kMaxThreads
      << "]\n\n";
  out << "Exit codes: 0 ok, 2 usage error, 3 input/LUT error,\n";
  out << "            4 render path deviation over threshold, 5 output error\n";
  return out.str();
}

namespace {


int emit(const Analysis& analysis, const Options& options) {
  if (options.json) {
    std::cout << analysis_json(analysis, false) << '\n';
  } else {
    std::cout << analysis_text(analysis);
  }
  if (!std::cout) {
    fail(ExitCode::Output, "cannot write report to stdout");
  }
  return report_exit_code(analysis);
}

Image load_or_fixture_image(const Options& options) {
  if (!options.input.empty()) {
    return read_ppm(options.input);
  }
  return make_fixture_image(kDefaultFixtureWidth, kDefaultFixtureHeight);
}

Lut3D load_or_fixture_lut(const Options& options) {
  if (!options.lut.empty()) {
    return load_cube_file(options.lut);
  }
  return load_cube_file(join_path("fixtures", "grade.cube"));
}

int report_and_write(const Image& image, const Lut3D& lut,
                     const Options& options, const std::string& output_path) {
  const Analysis analysis = run_analysis(image, lut, options);
  const int code = emit(analysis, options);
  if (!output_path.empty()) {
    write_ppm_atomic(output_path, analysis.rendered);
  }
  return code;
}

int run_demo(const Options& options) {
  if (options.generate_fixtures) {
    if (!ensure_directory("fixtures")) {
      fail(ExitCode::Output, "fixtures: path is not a directory");
    }
    write_fixture_lut(join_path("fixtures", "grade.cube"), options.grid_size);
    const Image image =
        make_fixture_image(kDefaultFixtureWidth, kDefaultFixtureHeight);
    write_fixture_image(join_path("fixtures", "sample.ppm"), image);
  }
  const Lut3D lut = load_or_fixture_lut(options);
  const Image image = load_or_fixture_image(options);
  return report_and_write(image, lut, options, options.output);
}

int run_render(const Options& options) {
  const Lut3D lut = load_cube_file(options.lut);
  const Image image = read_ppm(options.input);
  return report_and_write(image, lut, options, options.output);
}

int run_parse(const Options& options) {
  const Lut3D lut = load_cube_file(options.lut);
  auto number = [](double value) {
    char buffer[64];
    const auto result = std::to_chars(buffer, buffer + sizeof(buffer), value,
                                      std::chars_format::general);
    if (result.ec != std::errc()) {
      fail(ExitCode::Output, "cannot format numeric value");
    }
    return std::string(buffer, result.ptr);
  };
  std::cout << "{\"title\":" << json_quote(lut.title)
            << ",\"grid_size\":" << lut.size
            << ",\"nodes\":" << lut.node_count() << ",\"domain_min\":["
            << number(lut.domain_min.r) << ',' << number(lut.domain_min.g)
            << ',' << number(lut.domain_min.b) << "],\"domain_max\":["
            << number(lut.domain_max.r) << ',' << number(lut.domain_max.g)
            << ',' << number(lut.domain_max.b) << "]}\n";
  if (!std::cout) {
    fail(ExitCode::Output, "cannot write report to stdout");
  }
  return static_cast<int>(ExitCode::Ok);
}

int run_batch(const Options& options) {
  if (!ensure_directory(options.out_dir)) {
    fail(ExitCode::Output,
         options.out_dir + ": cannot use as output directory");
  }
  const Lut3D lut = load_cube_file(options.lut);
  int worst = static_cast<int>(ExitCode::Ok);
  for (std::size_t index = 0; index < options.inputs.size(); ++index) {
    const std::string& path = options.inputs[index];
    const Image image = read_ppm(path);
    const Analysis analysis = run_analysis(image, lut, options);
    if (options.json) {
      std::cout << analysis_json(analysis, false) << '\n';
    } else {
      std::cout << path << ": " << analysis_text(analysis);
    }
    if (!std::cout) {
      fail(ExitCode::Output, "cannot write batch report");
    }
    if (options.write_rendered) {
      // Each item is written atomically; a failure aborts the batch before any
      // later item starts, so no partially written file is ever left behind.
      const std::string name = "render_" + std::to_string(index) + ".ppm";
      write_ppm_atomic(join_path(options.out_dir, name), analysis.rendered);
    }
    if (analysis.deviation_exceeded) {
      worst = static_cast<int>(ExitCode::Deviation);
    }
  }
  return worst;
}

}  // namespace

int run(const Options& options) {
  if (options.command == "demo") return run_demo(options);
  if (options.command == "render") return run_render(options);
  if (options.command == "parse") return run_parse(options);
  if (options.command == "batch") return run_batch(options);
  fail(ExitCode::Usage, "unknown command '" + options.command + "'");
}

}  // namespace lutcheck

