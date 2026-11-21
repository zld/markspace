#include "cli.hpp"

#include <algorithm>
#include <cstdio>
#include <cstring>
#include <filesystem>
#include <map>
#include <string>
#include <vector>

#include "box_reader.hpp"
#include "fixture_builder.hpp"
#include "fragment_index.hpp"
#include "init_compat.hpp"
#include "repack.hpp"
#include "validator.hpp"

namespace mp4flow {
namespace fs = std::filesystem;

namespace {

constexpr int EX_OK = 0;
constexpr int EX_INIT_INCOMPAT = 2;
constexpr int EX_LAYOUT = 3;
constexpr int EX_MISSING = 4;
constexpr int EX_IO = 5;

std::map<std::string, std::string> parseFlags(int argc, char** argv) {
  std::map<std::string, std::string> out;
  for (int i = 0; i < argc; ++i) {
    std::string a = argv[i];
    if (a.rfind("--", 0) == 0) {
      auto eq = a.find('=');
      if (eq != std::string::npos)
        out[a.substr(2, eq - 2)] = a.substr(eq + 1);
      else if (i + 1 < argc)
        out[a.substr(2)] = argv[++i];
      else
        out[a.substr(2)] = "";
    }
  }
  return out;
}

std::vector<std::string> listFiles(const std::string& dir) {
  std::vector<std::string> out;
  std::error_code ec;
  if (!fs::is_directory(dir, ec))
    throw Mp4Error(ErrorKind::Io, dir, "init directory missing");
  for (const auto& e : fs::directory_iterator(dir, ec))
    if (e.is_regular_file(ec)) out.push_back(e.path().string());
  std::sort(out.begin(), out.end());
  return out;
}

std::vector<InitSignature> loadInits(const std::string& dir,
                                     InitCompatReport& compat,
                                     size_t& count) {
  std::vector<InitSignature> inits;
  for (const std::string& p : listFiles(dir))
    inits.push_back(extractInit(parseFile(p)));
  count = inits.size();
  compat = compareInits(inits);
  return inits;
}

int mapError(const Mp4Error& e) {
  switch (e.kind) {
  case ErrorKind::Io:
    return EX_IO;
  case ErrorKind::LengthOverflow:
  case ErrorKind::IllegalNesting:
  case ErrorKind::BadValue:
  case ErrorKind::Truncated:
    return EX_LAYOUT;
  }
  return EX_LAYOUT;
}

void printError(const Mp4Error& e) {
  std::fprintf(stderr, "error kind=%d box=%s :: %s\n", int(e.kind),
               e.path.c_str(), e.what());
}

int cmdFixtures(const std::map<std::string, std::string>& f) {
  if (!f.count("work")) { std::fprintf(stderr, "missing --work\n"); return EX_IO; }
  std::string tc = f.count("case") ? f.at("case") : "valid";
  FixtureCase c = FixtureCase::Valid;
  if (tc == "valid") c = FixtureCase::Valid;
  else if (tc == "gap") c = FixtureCase::Gap;
  else if (tc == "bad-init") c = FixtureCase::BadInit;
  else if (tc == "encrypted") c = FixtureCase::Encrypted;
  else { std::fprintf(stderr, "unknown --case %s\n", tc.c_str()); return EX_LAYOUT; }
  FixtureLayout lay = generateCase(f.at("work"), c);
  std::printf("fixture_case=%s\n", tc.c_str());
  for (const auto& p : lay.initFiles) std::printf("init_file=%s\n", p.c_str());
  for (const auto& p : lay.mediaFiles) std::printf("media_file=%s\n", p.c_str());
  return EX_OK;
}

void printCompat(const InitCompatReport& c, size_t initCount) {
  std::printf("init_compatible=%zu/%zu\n",
              c.compatible ? initCount : size_t(0), initCount);
  for (const FieldDiff& d : c.diffs)
    std::printf("init_diff track=%u field=%s a=%s b=%s\n", d.trackId,
                d.field.c_str(), d.valueA.c_str(), d.valueB.c_str());
  for (const std::string& e : c.encryptedRejections)
    std::printf("encrypted_rejected=%s\n", e.c_str());
}

// Parse FIRST:LAST used by --expect-seq.
bool parseRange(const std::string& s, uint32_t& first, uint32_t& last) {
  auto colon = s.find(':');
  if (colon == std::string::npos) return false;
  try {
    first = uint32_t(std::stoul(s.substr(0, colon)));
    last = uint32_t(std::stoul(s.substr(colon + 1)));
  } catch (...) { return false; }
  return last >= first;
}

int cmdRepack(const std::map<std::string, std::string>& f) {
  InitCompatReport compat;
  size_t initCount = 0;
  std::vector<InitSignature> inits =
      loadInits(f.at("init-dir"), compat, initCount);
  printCompat(compat, initCount);
  if (!compat.compatible) return EX_INIT_INCOMPAT;

  RepackOptions opt;
  opt.initDir = f.at("init-dir");
  opt.mediaDir = f.at("media-dir");
  opt.outDir = f.at("out");
  if (f.count("max-correction-ms"))
    opt.maxCorrectionMs = uint64_t(std::stoull(f.at("max-correction-ms")));
  if (f.count("expect-seq")) {
    if (!parseRange(f.at("expect-seq"), opt.expectFirst, opt.expectLast)) {
      std::fprintf(stderr, "bad --expect-seq, want FIRST:LAST\n");
      return EX_LAYOUT;
    }
    opt.hasExpectedRange = true;
  }

  FragmentIndex idx = buildIndex(opt.mediaDir, inits);
  validateSampleRanges(idx);
  if (opt.hasExpectedRange) {
    std::vector<uint32_t> missing =
        idx.missingSequences(opt.expectFirst, opt.expectLast);
    for (uint32_t s : missing)
      std::printf("missing_moof_seq=%u\n", s);
    if (!missing.empty())
      return EX_MISSING;
  }
  RepackReport rep = repack(opt, inits, idx);
  std::printf("input_fragments=%zu\n", rep.inputFragments);
  std::printf("input_moofs=%zu\n", rep.inputMoofs);
  std::printf("output_fragments=%zu\n", rep.outputFragments);
  std::printf("output_moofs=%zu\n", rep.outputMoofs);
  std::printf("moof_seq=%u..%u continuous=%s\n", rep.firstOutSeq,
              rep.lastOutSeq, rep.continuous ? "yes" : "no");
  for (const TrackRebaseStat& t : rep.tracks)
    std::printf("track=%u timescale=%u max_abs_decode_delta_ticks=%lld\n",
                t.trackId, t.timescale,
                (long long)t.maxAbsDecodeDeltaTicks);
  return EX_OK;
}

int cmdVerify(const std::map<std::string, std::string>& f) {
  InitCompatReport compat;
  size_t initCount = 0;
  std::vector<InitSignature> inits =
      loadInits(f.at("init-dir"), compat, initCount);
  printCompat(compat, initCount);
  if (!compat.compatible) return EX_INIT_INCOMPAT;

  VerifyOptions opt;
  opt.initDir = f.at("init-dir");
  opt.mediaDir = f.at("media-dir");
  if (f.count("expect-seq")) {
    if (!parseRange(f.at("expect-seq"), opt.expectFirst, opt.expectLast)) {
      std::fprintf(stderr, "bad --expect-seq, want FIRST:LAST\n");
      return EX_LAYOUT;
    }
    opt.hasExpectedRange = true;
  }
  VerifyReport rep = verifyMedia(opt, inits);
  std::printf("fragment_files=%zu moof_count=%zu sample_ranges_valid=%s\n",
              rep.fragmentFiles, rep.moofCount,
              rep.sampleRangesValid ? "yes" : "no");
  for (uint32_t s : rep.missing)
    std::printf("missing_moof_seq=%u\n", s);
  for (const SeqConflict& c : rep.duplicates)
    std::printf("duplicate_moof_seq=%u files=%s,%s\n", c.sequence,
                c.fileA.c_str(), c.fileB.c_str());
  if (!rep.missing.empty()) return EX_MISSING;
  std::printf("verify=ok\n");
  return EX_OK;
}

int usage() {
  std::fprintf(stderr,
      "usage:\n"
      "  mp4flow fixtures --work DIR --case valid|gap|bad-init|encrypted\n"
      "  mp4flow repack --init-dir DIR --media-dir DIR --out DIR\n"
      "                  [--expect-seq FIRST:LAST] [--max-correction-ms N]\n"
      "  mp4flow verify --init-dir DIR --media-dir DIR\n"
      "                  [--expect-seq FIRST:LAST]\n");
  return EX_IO;
}

} // namespace

int runCli(int argc, char** argv) {
  if (argc < 1) return usage();
  std::string cmd = argv[0];
  auto flags = parseFlags(argc - 1, argv + 1);
  try {
    if (cmd == "fixtures") return cmdFixtures(flags);
    if (cmd == "repack") {
      if (!flags.count("init-dir") || !flags.count("media-dir") ||
          !flags.count("out"))
        return usage();
      return cmdRepack(flags);
    }
    if (cmd == "verify") {
      if (!flags.count("init-dir") || !flags.count("media-dir"))
        return usage();
      return cmdVerify(flags);
    }
    return usage();
  } catch (Mp4Error& e) {
    printError(e);
    return mapError(e);
  } catch (std::exception& e) {
    std::fprintf(stderr, "error :: %s\n", e.what());
    return EX_IO;
  }
}

} // namespace mp4flow
