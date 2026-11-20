#include <cstdio>
#include <exception>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

#include "h264/analyzer.hpp"
#include "h264/fixture_writer.hpp"
#include "h264/self_test.hpp"

namespace {

constexpr int kExitOk = 0;
constexpr int kExitUsage = 2;
constexpr int kExitDamaged = 3;
constexpr int kExitSelfTest = 4;

void print_usage(std::FILE* stream) {
  std::fprintf(stream,
               "h264scan - Annex-B H.264 parameter and GOP analyzer\n"
               "\n"
               "Usage:\n"
               "  h264scan analyze --input <file> [--json] [--recover]\n"
               "  h264scan fixture --out <file> "
               "[--profile interlaced-multislice|baseline]\n"
               "                  [--three-byte-start-codes] "
               "[--leading-zeros N]\n"
               "  h264scan self-test [--verbose]\n"
               "\n"
               "Exit codes: 0 ok, 2 bad arguments/file, 3 damaged stream,\n"
               "            4 self-test failure\n");
}

bool read_file(const std::string& path, std::vector<std::uint8_t>* out,
               std::string* error) {
  std::ifstream in(path, std::ios::binary);
  if (!in) {
    *error = "cannot open input file: " + path;
    return false;
  }
  in.seekg(0, std::ios::end);
  const std::streamoff length = in.tellg();
  if (length < 0) {
    *error = "cannot determine size of input file: " + path;
    return false;
  }
  in.seekg(0, std::ios::beg);
  out->resize(static_cast<std::size_t>(length));
  if (length > 0) {
    in.read(reinterpret_cast<char*>(out->data()), length);
    if (!in) {
      *error = "failed to read input file: " + path;
      return false;
    }
  }
  return true;
}

bool write_file(const std::string& path, const std::vector<std::uint8_t>& data,
                std::string* error) {
  std::ofstream out(path, std::ios::binary | std::ios::trunc);
  if (!out) {
    *error = "cannot open output file: " + path;
    return false;
  }
  if (!data.empty()) {
    out.write(reinterpret_cast<const char*>(data.data()),
              static_cast<std::streamsize>(data.size()));
  }
  if (!out) {
    *error = "failed to write output file: " + path;
    return false;
  }
  return true;
}

int run_analyze(int argc, char** argv) {
  std::string input;
  bool json = false;
  bool recover = false;
  for (int i = 0; i < argc; ++i) {
    const std::string arg = argv[i];
    if (arg == "--input") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "error: --input requires a path\n");
        return kExitUsage;
      }
      input = argv[++i];
    } else if (arg == "--json") {
      json = true;
    } else if (arg == "--recover") {
      recover = true;
    } else if (arg == "--strict") {
      recover = false;
    } else {
      std::fprintf(stderr, "error: unknown analyze argument: %s\n",
                   arg.c_str());
      return kExitUsage;
    }
  }
  if (input.empty()) {
    std::fprintf(stderr, "error: analyze requires --input <file>\n");
    return kExitUsage;
  }

  std::vector<std::uint8_t> data;
  std::string error;
  if (!read_file(input, &data, &error)) {
    std::fprintf(stderr, "error: %s\n", error.c_str());
    return kExitUsage;
  }

  h264::AnalysisOptions options;
  options.mode = recover ? h264::AnalyzeMode::kRecovery
                         : h264::AnalyzeMode::kStrict;
  try {
    const h264::AnalysisReport report = h264::analyze_stream(data, options);
    if (json) {
      std::cout << h264::format_json_report(report, input) << "\n";
    } else {
      std::cout << h264::format_text_report(report, input);
    }
    // Recovery mode completes on damaged input by design: it exits 0 and
    // carries `incomplete: true` so build scripts can inspect the flag.
    if (report.ok) return kExitOk;
    return recover ? kExitOk : kExitDamaged;
  } catch (const h264::ParseError& parse_error) {
    const h264::Diagnostic& diag = parse_error.diagnostic();
    if (json) {
      std::printf(
          "{\"tool\":\"h264scan\",\"ok\":false,\"incomplete\":false,"
          "\"error\":\"%s\",\"nal_offset\":%zu}\n",
          diag.message.c_str(), diag.nal_offset);
    }
    std::fprintf(stderr, "error: NAL offset %zu: %s\n", diag.nal_offset,
                 diag.message.c_str());
    return kExitDamaged;
  }
}

int run_fixture(int argc, char** argv) {
  std::string out_path;
  h264::FixtureOptions options;
  for (int i = 0; i < argc; ++i) {
    const std::string arg = argv[i];
    if (arg == "--out") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "error: --out requires a path\n");
        return kExitUsage;
      }
      out_path = argv[++i];
    } else if (arg == "--profile") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "error: --profile requires a name\n");
        return kExitUsage;
      }
      const std::string name = argv[++i];
      if (!h264::parse_fixture_profile(name, &options.profile)) {
        std::fprintf(stderr, "error: unknown fixture profile: %s\n",
                     name.c_str());
        return kExitUsage;
      }
    } else if (arg == "--three-byte-start-codes") {
      options.use_four_byte_start_codes = false;
    } else if (arg == "--leading-zeros") {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "error: --leading-zeros requires a count\n");
        return kExitUsage;
      }
      try {
        options.leading_zero_bytes =
            static_cast<std::size_t>(std::stoul(argv[++i]));
      } catch (const std::exception&) {
        std::fprintf(stderr, "error: invalid --leading-zeros value\n");
        return kExitUsage;
      }
    } else {
      std::fprintf(stderr, "error: unknown fixture argument: %s\n",
                   arg.c_str());
      return kExitUsage;
    }
  }
  if (out_path.empty()) {
    std::fprintf(stderr, "error: fixture requires --out <file>\n");
    return kExitUsage;
  }

  const std::vector<std::uint8_t> bytes = h264::build_fixture(options);
  std::string error;
  if (!write_file(out_path, bytes, &error)) {
    std::fprintf(stderr, "error: %s\n", error.c_str());
    return kExitUsage;
  }
  std::printf("wrote %zu bytes (%s) to %s\n", bytes.size(),
              h264::fixture_profile_name(options.profile).c_str(),
              out_path.c_str());
  return kExitOk;
}

int run_self_test(int argc, char** argv) {
  bool verbose = false;
  for (int i = 0; i < argc; ++i) {
    const std::string arg = argv[i];
    if (arg == "--verbose") {
      verbose = true;
    } else {
      std::fprintf(stderr, "error: unknown self-test argument: %s\n",
                   arg.c_str());
      return kExitUsage;
    }
  }
  return h264::run_self_tests(verbose) == 0 ? kExitOk : kExitSelfTest;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc < 2) {
    print_usage(stderr);
    return kExitUsage;
  }
  const std::string command = argv[1];
  if (command == "analyze") return run_analyze(argc - 2, argv + 2);
  if (command == "fixture") return run_fixture(argc - 2, argv + 2);
  if (command == "self-test") return run_self_test(argc - 2, argv + 2);
  if (command == "--help" || command == "-h" || command == "help") {
    print_usage(stdout);
    return kExitOk;
  }
  std::fprintf(stderr, "error: unknown command: %s\n", command.c_str());
  print_usage(stderr);
  return kExitUsage;
}
