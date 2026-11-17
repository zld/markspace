// `fmp4tool verify` implementation. Kept in its own translation unit so the
// CLI stays within the per-batch size budget.
#include <fstream>
#include <iostream>
#include <map>
#include <nlohmann/json.hpp>
#include <string>

#include "fmp4/errors.hpp"
#include "fmp4/fragment_index.hpp"
#include "fmp4/validator.hpp"

using fmp4::Fmp4Error;
using fmp4::StructuralError;
using nlohmann::json;

namespace {

const std::string &need(const std::map<std::string, std::string> &a,
                        const std::string &k) {
  auto it = a.find(k);
  if (it == a.end()) throw Fmp4Error("missing required option --" + k);
  return it->second;
}

json issue_json(const fmp4::Issue &i) {
  return {{"kind", fmp4::issue_kind_name(i.kind)},
          {"file", i.file},
          {"offset", i.offset},
          {"sequence", i.sequence},
          {"detail", i.detail}};
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
    j["decode_duration_delta_ticks"] = t.duration_delta;
  }
  return j;
}

std::string join_seqs(const std::vector<uint32_t> &v) {
  std::string s;
  for (size_t i = 0; i < v.size(); ++i) {
    if (i) s += ",";
    s += std::to_string(v[i]);
  }
  return s;
}

} // namespace

int cmd_verify(const std::map<std::string, std::string> &args) {
  const std::string &input = need(args, "input");

  // build_index throws on corrupt boxes (exit 3); missing files are recorded.
  fmp4::FragmentIndex idx;
  try {
    idx = fmp4::build_index(input);
  } catch (const Fmp4Error &e) {
    throw StructuralError(e);
  }

  bool have_baseline = args.count("baseline") != 0;
  fmp4::FragmentIndex base;
  fmp4::ValidationReport rep;
  if (have_baseline) {
    try {
      base = fmp4::build_index(need(args, "baseline"));
    } catch (const Fmp4Error &e) {
      throw StructuralError(e);
    }
    rep = fmp4::validate_against_baseline(idx, base);
  } else {
    rep = fmp4::validate_index(idx);
  }

  json j;
  j["input_fragment_count"] = idx.fragments.size();
  j["present_fragment_count"] =
      uint32_t(idx.fragments.size() - idx.missing_sequences.size());
  j["missing_sequences"] = idx.missing_sequences;
  j["duplicate_sequences"] = idx.duplicate_sequences;
  j["mfhd_contiguous"] = rep.mfhd_contiguous;
  j["mfhd_gaps"] = rep.mfhd_gaps;
  j["mfhd_sequences"] = rep.mfhd_sequences;
  json tracks = json::array();
  for (const auto &t : rep.timelines)
    tracks.push_back(timeline_json(t, have_baseline));
  j["tracks"] = std::move(tracks);
  json issues = json::array();
  for (const auto &i : rep.issues) issues.push_back(issue_json(i));
  j["issues"] = std::move(issues);

  std::cout << "input_fragment_count=" << idx.fragments.size() << "\n";
  std::cout << "missing_sequences=[" << join_seqs(idx.missing_sequences)
            << "]\n";
  std::cout << "duplicate_sequences=[" << join_seqs(idx.duplicate_sequences)
            << "]\n";
  std::cout << "mfhd_contiguous=" << (rep.mfhd_contiguous ? "true" : "false")
            << "\n";
  std::cout << "mfhd_gaps=[" << join_seqs(rep.mfhd_gaps) << "]\n";
  for (const auto &t : rep.timelines) {
    std::cout << "track_id=" << t.track_id
              << " timescale=" << t.timescale
              << " decode_start=" << t.decode_start << " ticks ("
              << fmp4::ticks_to_seconds(t.decode_start, t.timescale)
              << " s) total_duration=" << t.total_duration << " ticks ("
              << fmp4::ticks_to_seconds(t.total_duration, t.timescale)
              << " s)";
    if (have_baseline)
      std::cout << " decode_start_delta_ticks=" << t.decode_start_delta
                << " decode_duration_delta_ticks=" << t.duration_delta;
    std::cout << "\n";
  }
  for (const auto &i : rep.issues)
    std::cerr << "issue[" << fmp4::issue_kind_name(i.kind) << "] "
              << (i.file.empty() ? "" : i.file + " ")
              << i.detail << "\n";

  if (args.count("json")) {
    std::ofstream os(args.at("json"),
                     std::ios::binary | std::ios::trunc);
    if (!os) throw Fmp4Error("cannot write JSON file: " + args.at("json"));
    os << j.dump(2) << "\n";
  }

  return rep.ok() ? 0 : 3;
}
