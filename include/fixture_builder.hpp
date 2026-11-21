#pragma once

#include "box_reader.hpp"

#include <cstdint>
#include <string>
#include <vector>

namespace mp4flow {

// Synthetic fMP4 fixture generator. All bytes are produced by code; no
// external media files are referenced. Payloads are deterministic dummy
// sample data, sized exactly as declared in trun.
struct FixtureTrack {
  uint32_t trackId;
  std::string handler;   // "vide" / "soun"
  uint32_t timescale;
  uint32_t sampleEntry;  // fourcc, e.g. 'avc1'
  uint32_t sampleDuration; // trex default duration per sample
  uint32_t sampleSize;    // trex default size per sample
};

struct FixtureFragment {
  uint32_t mfhdSeq;
  // Per-track decode times as written (may intentionally reset/duplicate).
  std::vector<uint64_t> tfdt;
  std::vector<uint32_t> sampleCounts; // per track
  int32_t dataOffsetBias = 0;         // perturb trun data_offset
  int64_t cttsOffset = 0;             // signed composition offset on sample 0
  uint8_t trunVersion = 0;            // 0 unsigned / 1 signed ctts
};

struct FixturePlan {
  std::vector<FixtureTrack> tracks;
  std::vector<FixtureFragment> fragments;
  // Fragments with this index are appended into the previous fragment's file
  // so one source file can contain several moof boxes.
  std::vector<size_t> multiMoofJoinAt;
  // When true, init B is generated with an incompatible timescale.
  bool incompatibleInit = false;
  // When true, an encrypted ('enca') sample entry is emitted.
  bool encryptedInit = false;
  std::string filePrefix = "frag";
};

// Two init segments (shared, compatible by default) under <work>/init and
// media fragments under <work>/media, prefixed with decimal ordinals.
// Returns the generated file paths.
struct FixtureLayout {
  std::vector<std::string> initFiles;
  std::vector<std::string> mediaFiles;
};
FixtureLayout generateFixture(const std::string& work, const FixturePlan& plan);

// Named demo scenarios used by the CLI "fixtures" subcommand.
enum class FixtureCase { Valid, Gap, BadInit, Encrypted };
FixturePlan makePlan(FixtureCase tc);
FixtureLayout generateCase(const std::string& work, FixtureCase tc);

} // namespace mp4flow
