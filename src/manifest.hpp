// Small line-oriented manifest written next to the PPM frames so `verify`
// can re-render with the identical parameters without any user input.
// Format is plain key=value / tab-separated rows, no external parser.
#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace tr {

struct ManifestEntry {
  std::string type;
  int frames;
  std::string firstHash;
  std::string lastHash;
};

struct Manifest {
  int width;
  int height;
  std::int64_t fpsNum;
  std::int64_t fpsDen;
  std::string sample;
  std::vector<ManifestEntry> entries;
};

constexpr const char* kManifestName = "render_manifest.txt";

void writeManifest(const std::string& dir, const Manifest& m);
Manifest readManifest(const std::string& dir);

}  // namespace tr
