#include <cstdio>
#include <cstring>
#include <fstream>
#include <string>

#include "avsync/common.hpp"
#include "avsync/metrics.hpp"
#include "avsync/pipeline.hpp"

namespace {
void print_usage() {
  std::fprintf(stderr,
               "usage: avsync run --input <file> [options]\n"
               "options:\n"
               "  --jitter-ms N         inject deterministic 0..N ms jitter\n"
               "  --seed N              fixed PRNG seed for replay\n"
               "  --realtime-factor X   timeline speed multiplier (default 1)\n"
               "  --json <path>         write full JSON report\n");
}

bool parse_int(const char* text, int& out) {
  try {
    size_t consumed = 0;
    const int value = std::stoi(text, &consumed);
    if (consumed != std::strlen(text)) return false;
    out = value;
    return true;
  } catch (...) {
    return false;
  }
}

bool parse_uint64(const char* text, uint64_t& out) {
  try {
    size_t consumed = 0;
    const unsigned long long value = std::stoull(text, &consumed);
    if (consumed != std::strlen(text)) return false;
    out = static_cast<uint64_t>(value);
    return true;
  } catch (...) {
    return false;
  }
}

bool parse_double(const char* text, double& out) {
  try {
    size_t consumed = 0;
    const double value = std::stod(text, &consumed);
    if (consumed != std::strlen(text)) return false;
    out = value;
    return true;
  } catch (...) {
    return false;
  }
}
}  // namespace

int main(int argc, char** argv) {
  if (argc < 2 || std::strcmp(argv[1], "run") != 0) {
    print_usage();
    return static_cast<int>(avsync::ExitCode::UsageOrInput);
  }

  avsync::Config config;
  for (int i = 2; i < argc; ++i) {
    const std::string flag = argv[i];
    auto require_value = [&](const char* name) -> const char* {
      if (i + 1 >= argc) {
        std::fprintf(stderr, "avsync: %s requires a value\n", name);
        return nullptr;
      }
      return argv[++i];
    };

    if (flag == "--input") {
      const char* value = require_value("--input");
      if (!value) return static_cast<int>(avsync::ExitCode::UsageOrInput);
      config.input_path = value;
    } else if (flag == "--json") {
      const char* value = require_value("--json");
      if (!value) return static_cast<int>(avsync::ExitCode::UsageOrInput);
      config.json_path = value;
    } else if (flag == "--jitter-ms") {
      const char* value = require_value("--jitter-ms");
      if (!value || !parse_int(value, config.jitter_ms) ||
          config.jitter_ms < 0) {
        std::fprintf(stderr, "avsync: invalid --jitter-ms value\n");
        return static_cast<int>(avsync::ExitCode::UsageOrInput);
      }
    } else if (flag == "--seed") {
      const char* value = require_value("--seed");
      if (!value || !parse_uint64(value, config.seed)) {
        std::fprintf(stderr, "avsync: invalid --seed value\n");
        return static_cast<int>(avsync::ExitCode::UsageOrInput);
      }
    } else if (flag == "--realtime-factor") {
      const char* value = require_value("--realtime-factor");
      if (!value || !parse_double(value, config.realtime_factor) ||
          config.realtime_factor <= 0.0) {
        std::fprintf(stderr, "avsync: invalid --realtime-factor value\n");
        return static_cast<int>(avsync::ExitCode::UsageOrInput);
      }
    } else {
      std::fprintf(stderr, "avsync: unknown argument '%s'\n", flag.c_str());
      print_usage();
      return static_cast<int>(avsync::ExitCode::UsageOrInput);
    }
  }

  avsync::Pipeline pipeline(config);
  std::string error;
  if (!pipeline.setup(&error)) {
    std::fprintf(stderr, "avsync: %s\n", error.c_str());
    return static_cast<int>(avsync::ExitCode::UsageOrInput);
  }

  const avsync::ExitCode code = pipeline.run();
  const avsync::Metrics& metrics = pipeline.metrics();

  if (code == avsync::ExitCode::Ok) {
    std::fputs(metrics.render_text().c_str(), stdout);
    if (config.json_path.has_value()) {
      std::ofstream out(*config.json_path);
      if (!out) {
        std::fprintf(stderr, "avsync: cannot write %s\n",
                     config.json_path->c_str());
        return static_cast<int>(avsync::ExitCode::UsageOrInput);
      }
      out << metrics.render_json(pipeline.input_path());
    }
  } else if (code == avsync::ExitCode::DecodeFailure) {
    std::fprintf(stderr, "avsync: decoding failed before completion\n");
  } else {
    std::fprintf(stderr, "avsync: worker thread failure\n");
  }
  return static_cast<int>(code);
}
