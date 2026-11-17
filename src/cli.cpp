// Terminal entry point. Exit codes:
//   0 success, 2 parameter/file error, 3 structural/missing/compatibility,
//   4 output commit failure.
#include <cstring>
#include <algorithm>
#include <fstream>
#include <iostream>
#include <map>
#include <nlohmann/json.hpp>
#include <string>

#include "fmp4/errors.hpp"
#include "fmp4/fixture_writer.hpp"
#include "fmp4/fragment_index.hpp"
#include "fmp4/repacker.hpp"
#include "fmp4/validator.hpp"

using fmp4::Fmp4Error;
using fmp4::RepackError;
using fmp4::StructuralError;
using nlohmann::json;

namespace {

struct Args {
  std::map<std::string, std::string> opt;
  std::map<std::string, bool> flags;
};

bool has(const Args &a, const std::string &k) {
  return a.opt.count(k) || a.flags.count(k);
}
const std::string &need(const Args &a, const std::string &k) {
  auto it = a.opt.find(k);
  if (it == a.opt.end()) throw Fmp4Error("missing required option --" + k);
  return it->second;
}
uint32_t need_u32(const Args &a, const std::string &k) {
  const std::string &s = need(a, k);
  try {
    unsigned long v = std::stoul(s);
    if (v > 0xfffffffful) throw std::out_of_range("u32");
    return uint32_t(v);
  } catch (const std::exception &) {
    throw Fmp4Error("option --" + k + " needs a non-negative integer");
  }
}
uint32_t opt_u32(const Args &a, const std::string &k, uint32_t dflt) {
  return has(a, k) ? need_u32(a, k) : dflt;
}

void usage() {
  std::cout <<
      "fmp4tool - fragmented MP4 repack and verification\n\n"
      "usage:\n"
      "  fmp4tool fixture --dir DIR [--segments N] [--mixed-versions]\n"
      "                    [--mfhd-gap-at SEQ] [--omit-file-for-seq SEQ]\n"
      "                    [--corrupt-length-at SEQ] [--incompatible-init]\n"
      "  fmp4tool repack --input DIR --output DIR [--json FILE]\n"
      "  fmp4tool verify --input DIR [--baseline DIR] [--json FILE]\n";
}

void dump_json_file(const std::string &path, const json &j) {
  std::ofstream os(path, std::ios::binary | std::ios::trunc);
  if (!os) throw Fmp4Error("cannot write JSON file: " + path);
  os << j.dump(2) << "\n";
}

int cmd_fixture(const Args &a) {
  fmp4::fixture::Options o;
  o.dir = need(a, "dir");
  o.segments = opt_u32(a, "segments", 4);
  if (o.segments == 0) throw Fmp4Error("--segments must be >= 1");
  o.mixed_versions = a.flags.count("mixed-versions") != 0;
  o.mfhd_gap_at = opt_u32(a, "mfhd-gap-at", 0);
  o.omit_file_for_seq = opt_u32(a, "omit-file-for-seq", 0);
  o.corrupt_length_at = opt_u32(a, "corrupt-length-at", 0);
  o.incompatible_init = a.flags.count("incompatible-init") != 0;
  auto seqs = fmp4::fixture::write_fixture(o);
  std::cout << "fixture written to " << o.dir << "\n";
  std::cout << "segments=" << o.segments
            << " mfhd_sequences=";
  for (size_t i = 0; i < seqs.size(); ++i)
    std::cout << (i ? "," : "") << seqs[i];
  std::cout << "\n";
  if (o.omit_file_for_seq)
    std::cout << "omitted_file_for_seq=" << o.omit_file_for_seq << "\n";
  return 0;
}

json timeline_json(const fmp4::TrackTimeline &t, bool with_delta) {
  json j = {{"track_id", t.track_id},
            {"timescale", t.timescale},
            {"decode_start_ticks", t.decode_start},
            {"decode_start_seconds",
             fmp4::ticks_to_seconds(t.decode_start, t.timescale)},
            {"total_duration_ticks", t.total_duration},
            {"total_duration_seconds",
             fmp4::ticks_to_seconds(t.total_duration, t.timescale)}};
  if (with_delta) {
    j["decode_start_delta_ticks"] = t.decode_start_delta;
    j["decode_start_delta_seconds"] =
        double(t.decode_start_delta) / double(t.timescale ? t.timescale : 1);
    j["decode_duration_delta_ticks"] = t.duration_delta;
    j["decode_duration_delta_seconds"] =
        double(t.duration_delta) / double(t.timescale ? t.timescale : 1);
  }
  return j;
}

int cmd_repack(const Args &a) {
  fmp4::RepackResult r =
      fmp4::repack_directory(need(a, "input"), need(a, "output"));
  json j;
  j["input_fragment_count"] = r.input_fragment_count;
  j["output_fragment_count"] = r.output_fragment_count;
  j["mfhd_before"] = r.mfhd_before;
  j["mfhd_after"] = r.mfhd_after;
  auto contiguous = [](const std::vector<uint32_t> &v) {
    for (size_t i = 0; i + 1 < v.size(); ++i)
      if (v[i + 1] != v[i] + 1) return false;
    return true;
  };
  std::vector<uint32_t> before_gaps;
  if (!r.mfhd_before.empty())
    for (uint32_t v = r.mfhd_before.front(); v < r.mfhd_before.back(); ++v)
      if (std::find(r.mfhd_before.begin(), r.mfhd_before.end(), v) ==
          r.mfhd_before.end())
        before_gaps.push_back(v);
  j["mfhd_before_contiguous"] = contiguous(r.mfhd_before);
  j["mfhd_before_gaps"] = before_gaps;
  j["mfhd_after_contiguous"] = contiguous(r.mfhd_after);
  j["mfhd_after_gaps"] = json::array();
  j["missing_sequences"] = json::array();
  json tracks = json::array();
  for (const auto &t : r.verification.timelines)
    tracks.push_back(timeline_json(t, true));
  j["tracks"] = std::move(tracks);
  if (has(a, "json")) dump_json_file(need(a, "json"), j);

  std::cout << "input_fragment_count=" << r.input_fragment_count << "\n";
  std::cout << "output_fragment_count=" << r.output_fragment_count << "\n";
  for (const auto &t : r.verification.timelines) {
    std::cout << "track_id=" << t.track_id
              << " timescale=" << t.timescale
              << " decode_start_delta_ticks=" << t.decode_start_delta
              << " decode_duration_delta_ticks=" << t.duration_delta
              << "\n";
  }
  return 0;
}

} // namespace

int cmd_verify(const std::map<std::string, std::string> &opt);

int main(int argc, char **argv) {
  if (argc < 2 || std::strcmp(argv[1], "--help") == 0 ||
      std::strcmp(argv[1], "-h") == 0) {
    usage();
    return argc < 2 ? 2 : 0;
  }
  std::string cmd = argv[1];
  Args args;
  for (int i = 2; i < argc; ++i) {
    std::string tok = argv[i];
    if (tok.rfind("--", 0) != 0)
      throw Fmp4Error("unexpected positional argument: " + tok);
    std::string key = tok.substr(2);
    // Boolean flags recognized by the fixture command.
    if (key == "mixed-versions" || key == "incompatible-init") {
      args.flags[key] = true;
      continue;
    }
    if (i + 1 >= argc) throw Fmp4Error("option --" + key + " needs a value");
    args.opt[key] = argv[++i];
  }
  try {
    if (cmd == "fixture") return cmd_fixture(args);
    if (cmd == "repack") return cmd_repack(args);
    if (cmd == "verify") return cmd_verify(args.opt);
    std::cerr << "error: unknown subcommand " << cmd << "\n";
    usage();
    return 2;
  } catch (const RepackError &e) {
    std::cerr << "error: " << e.what() << "\n";
    return e.exit_code();
  } catch (const StructuralError &e) {
    std::cerr << "error: " << e.what() << "\n";
    return 3;
  } catch (const Fmp4Error &e) {
    std::cerr << "error: " << e.what() << "\n";
    return 2;
  } catch (const std::exception &e) {
    std::cerr << "error: " << e.what() << "\n";
    return 2;
  }
}
