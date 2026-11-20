#include "fmp4/repacker.hpp"

#include <algorithm>
#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <map>
#include <set>

namespace fmp4 {
namespace fs = std::filesystem;

std::vector<TrackDeltas> compute_decode_deltas(
    const std::vector<Fragment>& frags, const InitInfo& init) {
  std::map<uint32_t, TrackDeltas> by_track;
  for (size_t i = 1; i < frags.size(); ++i) {
    for (const auto& ta : frags[i - 1].trafs) {
      const Traf* tb = nullptr;
      for (const auto& t : frags[i].trafs)
        if (t.tfhd.track_id == ta.tfhd.track_id) tb = &t;
      if (!tb) continue;
      if (!ta.tfdt || !tb->tfdt)
        throw BoxError(frags[i].file, frags[i].moof_offset,
                       "missing tfdt for track " +
                           std::to_string(ta.tfhd.track_id));
      const InitTrack* track = init.find_track(ta.tfhd.track_id);
      const TrexDefaults* trex =
          (track && track->has_trex) ? &track->trex : nullptr;
      uint64_t dur = traf_duration(ta, trex, frags[i - 1].file);
      int64_t delta = static_cast<int64_t>(tb->tfdt->value) -
                      (static_cast<int64_t>(ta.tfdt->value) +
                       static_cast<int64_t>(dur));
      TrackDeltas& td = by_track[ta.tfhd.track_id];
      td.track_id = ta.tfhd.track_id;
      td.decode_delta_ticks.push_back(delta);
      td.next_seq.push_back(frags[i].sequence);
    }
  }
  std::vector<TrackDeltas> out;
  for (auto& kv : by_track) out.push_back(std::move(kv.second));
  return out;
}

namespace {

std::vector<std::string> list_segment_files(const std::string& dir) {
  std::error_code ec;
  if (!fs::is_directory(dir, ec))
    throw Fmp4Error(kExitUsage, "segments directory not found: " + dir);
  std::vector<std::string> files;
  for (const auto& e : fs::directory_iterator(dir, ec))
    if (e.is_regular_file()) files.push_back(e.path().string());
  if (ec) throw Fmp4Error(kExitUsage, "cannot list directory: " + dir);
  std::sort(files.begin(), files.end());  // stable filename order
  return files;
}

void put_be32(std::vector<uint8_t>& buf, size_t pos, uint32_t v) {
  buf[pos] = (uint8_t)(v >> 24);
  buf[pos + 1] = (uint8_t)(v >> 16);
  buf[pos + 2] = (uint8_t)(v >> 8);
  buf[pos + 3] = (uint8_t)v;
}

void put_be64(std::vector<uint8_t>& buf, size_t pos, uint64_t v) {
  put_be32(buf, pos, (uint32_t)(v >> 32));
  put_be32(buf, pos + 4, (uint32_t)v);
}

std::string json_u32s(const std::vector<uint32_t>& v) {
  std::string s = "[";
  for (size_t i = 0; i < v.size(); ++i) {
    if (i) s += ", ";
    s += std::to_string(v[i]);
  }
  return s + "]";
}

std::string json_deltas(const std::vector<TrackDeltas>& v) {
  std::string s = "[";
  for (size_t i = 0; i < v.size(); ++i) {
    if (i) s += ", ";
    s += "{\"track_id\": " + std::to_string(v[i].track_id) +
         ", \"decode_delta_ticks\": [";
    for (size_t j = 0; j < v[i].decode_delta_ticks.size(); ++j) {
      if (j) s += ", ";
      s += std::to_string(v[i].decode_delta_ticks[j]);
    }
    s += "]}";
  }
  return s + "]";
}

std::string to_json(const RepackSummary& s) {
  std::string j = "{\n";
  j += "  \"status\": \"ok\",\n";
  j += "  \"output_file\": \"" + s.output_file + "\",\n";
  j += "  \"before_fragments\": " + std::to_string(s.before_fragments) + ",\n";
  j += "  \"after_fragments\": " + std::to_string(s.after_fragments) + ",\n";
  j += std::string("  \"before_moof_contiguous\": ") +
       (s.before_moof_contiguous ? "true" : "false") + ",\n";
  j += std::string("  \"after_moof_contiguous\": ") +
       (s.after_moof_contiguous ? "true" : "false") + ",\n";
  j += "  \"missing_moof_seq\": " + json_u32s(s.missing) + ",\n";
  j += "  \"duplicate_moof_seq\": " + json_u32s(s.duplicates) + ",\n";
  j += "  \"out_of_order_moof_seq\": " + json_u32s(s.out_of_order) + ",\n";
  j += "  \"before_track_decode_delta_ticks\": " + json_deltas(s.before_deltas) +
       ",\n";
  j += "  \"after_track_decode_delta_ticks\": " + json_deltas(s.after_deltas) +
       "\n}";
  return j;
}

bool check_contiguous(const std::vector<Fragment>& frags, uint32_t first_seq) {
  for (size_t i = 0; i < frags.size(); ++i)
    if (frags[i].sequence != first_seq + i) return false;
  return true;
}

void print_issues(const std::vector<InitIssue>& issues) {
  for (const auto& i : issues)
    std::fprintf(stderr, "INIT_INCOMPATIBLE track=%u field=%s: %s\n",
                 i.track_id, i.field.c_str(), i.message.c_str());
}

int run_repack_impl(const RepackOptions& opt) {
  InitInfo init = parse_init(opt.init_path);
  auto init_issues = validate_init(init);
  if (!init_issues.empty()) {
    print_issues(init_issues);
    return kExitInit;
  }

  std::vector<Fragment> all;
  std::vector<uint32_t> encounter_order;
  for (const auto& f : list_segment_files(opt.segments_dir)) {
    SegmentIndex idx = build_segment_index(f);
    for (auto& frag : idx.fragments) {
      encounter_order.push_back(frag.sequence);
      all.push_back(std::move(frag));
    }
  }

  std::set<uint32_t> seen;
  std::vector<uint32_t> dups;
  for (const auto& fr : all)
    if (!seen.insert(fr.sequence).second) dups.push_back(fr.sequence);
  if (!dups.empty()) {
    for (uint32_t d : dups) std::printf("DUPLICATE_MOOF_SEQ=%u\n", d);
    return kExitSequence;
  }

  RepackSummary sum;
  for (size_t i = 1; i < encounter_order.size(); ++i)
    if (encounter_order[i] < encounter_order[i - 1])
      sum.out_of_order.push_back(encounter_order[i]);

  std::vector<Fragment> frags;
  for (auto& fr : all)
    if (fr.sequence >= opt.first_seq && fr.sequence <= opt.last_seq)
      frags.push_back(std::move(fr));
  for (uint32_t s = opt.first_seq; s <= opt.last_seq; ++s) {
    bool found = false;
    for (const auto& fr : frags)
      if (fr.sequence == s) found = true;
    if (!found) sum.missing.push_back(s);
  }
  if (!sum.missing.empty()) {
    for (uint32_t m : sum.missing) std::printf("MISSING_MOOF_SEQ=%u\n", m);
    return kExitSequence;
  }
  std::sort(frags.begin(), frags.end(),
            [](const Fragment& a, const Fragment& b) {
              return a.sequence < b.sequence;
            });

  auto frag_issues = validate_fragments(init, frags);
  if (!frag_issues.empty()) {
    print_issues(frag_issues);
    return kExitInit;
  }

  sum.before_fragments = frags.size();
  sum.before_moof_contiguous = check_contiguous(frags, opt.first_seq);
  sum.before_deltas = compute_decode_deltas(frags, init);
  for (const auto& td : sum.before_deltas)
    for (size_t i = 0; i < td.decode_delta_ticks.size(); ++i)
      if (std::llabs(td.decode_delta_ticks[i]) > opt.max_decode_delta_ticks) {
        std::fprintf(stderr,
                     "DECODE_DELTA_EXCEEDED track=%u boundary_seq=%u "
                     "delta=%lld\n",
                     td.track_id, td.next_seq[i],
                     (long long)td.decode_delta_ticks[i]);
        return kExitDelta;
      }

  // Layout: init bytes, then moof+mdat pairs in sequence order.
  auto init_bytes = read_file(opt.init_path);
  std::map<std::string, std::vector<uint8_t>> cache;
  auto load = [&](const std::string& p) -> const std::vector<uint8_t>& {
    auto it = cache.find(p);
    if (it == cache.end()) it = cache.emplace(p, read_file(p)).first;
    return it->second;
  };
  std::vector<uint64_t> out_moof(frags.size()), out_mdat(frags.size());
  uint64_t off = init_bytes.size();
  for (size_t i = 0; i < frags.size(); ++i) {
    out_moof[i] = off;
    off += frags[i].moof_size;
    out_mdat[i] = off;
    off += frags[i].mdat_size;
  }

  std::vector<uint8_t> out = init_bytes;
  out.reserve(static_cast<size_t>(off));
  for (size_t i = 0; i < frags.size(); ++i) {
    const Fragment& frag = frags[i];
    const auto& src = load(frag.file);
    std::vector<uint8_t> moof(src.begin() + frag.moof_offset,
                              src.begin() + frag.moof_offset + frag.moof_size);
    uint64_t mdat_hdr = frag.mdat_payload_offset - frag.mdat_offset;
    for (const auto& traf : frag.trafs) {
      uint64_t base_orig = 0;
      if (traf.tfhd.base_data_offset) {
        base_orig = *traf.tfhd.base_data_offset;
      } else if (traf.tfhd.default_base_is_moof) {
        base_orig = frag.moof_offset;
      } else {
        throw BoxError(frag.file, traf.tfhd.box_offset,
                       "unsupported tfhd base offset mode");
      }
      if (traf.tfhd.base_data_offset) {
        // Rebase to the new moof position so trun offsets stay moof-relative.
        size_t pos = static_cast<size_t>(traf.tfhd.base_data_offset_field -
                                         frag.moof_offset);
        put_be64(moof, pos, out_moof[i]);
      }
      for (const auto& trun : traf.truns) {
        if (!trun.data_offset) continue;
        int64_t sample_start =
            static_cast<int64_t>(base_orig) + *trun.data_offset;
        int64_t within =
            sample_start - static_cast<int64_t>(frag.mdat_payload_offset);
        if (within < 0 ||
            static_cast<uint64_t>(within) >= frag.mdat_size - mdat_hdr)
          throw BoxError(frag.file, trun.box_offset,
                         "trun data offset outside mdat payload");
        int64_t new_off =
            static_cast<int64_t>(out_mdat[i] + mdat_hdr +
                                 static_cast<uint64_t>(within)) -
            static_cast<int64_t>(out_moof[i]);
        if (new_off < INT32_MIN || new_off > INT32_MAX)
          throw BoxError(frag.file, trun.box_offset,
                         "data offset does not fit int32");
        size_t pos = static_cast<size_t>(trun.data_offset_field -
                                         frag.moof_offset);
        put_be32(moof, pos, static_cast<uint32_t>((int32_t)new_off));
      }
    }
    out.insert(out.end(), moof.begin(), moof.end());
    out.insert(out.end(), src.begin() + frag.mdat_offset,
               src.begin() + frag.mdat_offset + frag.mdat_size);
  }

  std::error_code ec;
  fs::create_directories(opt.out_dir, ec);
  if (ec) throw Fmp4Error(kExitUsage, "cannot create out dir: " + opt.out_dir);
  std::string final_path = (fs::path(opt.out_dir) / "repacked.mp4").string();
  std::string tmp_path = (fs::path(opt.out_dir) / ".repacked.tmp").string();
  try {
    write_file(tmp_path, out);
    // Full re-parse of the candidate output before committing it.
    SegmentIndex verify = build_segment_index(tmp_path);
    std::sort(verify.fragments.begin(), verify.fragments.end(),
              [](const Fragment& a, const Fragment& b) {
                return a.sequence < b.sequence;
              });
    sum.after_fragments = verify.fragments.size();
    sum.after_moof_contiguous =
        check_contiguous(verify.fragments, opt.first_seq);
    sum.after_deltas = compute_decode_deltas(verify.fragments, init);
    fs::rename(tmp_path, final_path, ec);
    if (ec)
      throw Fmp4Error(kExitUsage, "cannot commit output: " + ec.message());
  } catch (...) {
    std::error_code ec2;
    fs::remove(tmp_path, ec2);  // never leave a half-written output
    throw;
  }

  sum.output_file = final_path;
  std::string json = to_json(sum);
  if (opt.json_stdout) {
    std::printf("%s\n", json.c_str());
  } else {
    std::printf("OUTPUT=%s\nBEFORE_FRAGMENTS=%llu\nAFTER_FRAGMENTS=%llu\n",
                final_path.c_str(), (unsigned long long)sum.before_fragments,
                (unsigned long long)sum.after_fragments);
  }
  if (!opt.report_path.empty())
    write_file(opt.report_path,
               std::vector<uint8_t>(json.begin(), json.end()));
  return kExitOk;
}

}  // namespace

int run_repack(const RepackOptions& opt) {
  try {
    return run_repack_impl(opt);
  } catch (const Fmp4Error& e) {
    std::fprintf(stderr, "ERROR: %s\n", e.what());
    return e.code;
  }
}

}  // namespace fmp4
