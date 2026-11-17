#include "fmp4/repacker.hpp"

#include <cstdio>
#include <filesystem>
#include <fstream>
#include <nlohmann/json.hpp>
#include <system_error>

#include "fmp4/safe_path.hpp"

namespace fmp4 {

using nlohmann::json;
namespace fs = std::filesystem;

namespace {

constexpr size_t kChunk = 64 * 1024;

void stream_copy_range(std::FILE *in, std::FILE *out, uint64_t begin,
                       uint64_t end) {
  if (fseeko(in, off_t(begin), SEEK_SET) != 0)
    throw Fmp4Error("seek failed during copy");
  std::vector<uint8_t> buf(kChunk);
  uint64_t remaining = end - begin;
  while (remaining) {
    size_t want = size_t(std::min<uint64_t>(remaining, buf.size()));
    size_t got = std::fread(buf.data(), 1, want, in);
    if (got == 0) throw Fmp4Error("unexpected EOF during copy");
    if (std::fwrite(buf.data(), 1, got, out) != got)
      throw RepackError(4, "write failed into temporary output");
    remaining -= got;
  }
}

std::FILE *open_wb(const fs::path &p) {
  std::FILE *fp = std::fopen(p.string().c_str(), "wb");
  if (!fp) throw RepackError(4, "cannot create output file: " + p.string());
  return fp;
}

} // namespace

RepackResult repack_directory(const std::string &input_dir,
                              const std::string &output_dir) {
  // 1. Index + structural validation of the input. Missing/duplicate
  // sequences block repacking; mfhd gaps are expected inputs that the
  // rewrite is meant to close, so they do not block.
  FragmentIndex idx = build_index(input_dir);
  if (!idx.missing_sequences.empty() || !idx.duplicate_sequences.empty()) {
    RepackError e(3, "input has missing or duplicate sequences, cannot repack");
    e.missing_sequences = idx.missing_sequences;
    throw e;
  }

  // 2. Path safety: no overlap; output must be fresh.
  std::string in_canon = idx.root_dir;
  std::string out_parent = path::canonical_output(output_dir);
  path::ensure_not_overlapping(in_canon, out_parent);
  fs::path out_p(out_parent);
  fs::path parent = out_p.parent_path();
  std::error_code ec;
  if (fs::exists(out_p, ec))
    throw Fmp4Error("output path already exists: " + out_parent);

  // 3. Temporary directory next to the final target.
  fs::path tmp = parent / (".fmp4tmp-" + out_p.filename().string());
  fs::remove_all(tmp, ec);
  if (!fs::create_directory(tmp, ec))
    throw RepackError(4, "cannot create temporary directory " + tmp.string());

  // On any failure below, remove the temp tree so nothing half-built remains.
  struct Guard {
    fs::path dir;
    bool committed = false;
    ~Guard() {
      if (!committed) {
        std::error_code e;
        fs::remove_all(dir, e);
      }
    }
  } guard{tmp};

  RepackResult res;
  res.input_fragment_count = uint32_t(idx.fragments.size());

  // 4. Copy the init segment byte-for-byte.
  fs::path init_name = fs::path(idx.init.path).filename();
  {
    std::FILE *in = std::fopen(idx.init.path.c_str(), "rb");
    if (!in) throw Fmp4Error("cannot open init: " + idx.init.path);
    std::FILE *out = open_wb(tmp / init_name);
    uint64_t sz = fs::file_size(idx.init.path, ec);
    stream_copy_range(in, out, 0, sz);
    std::fclose(in);
    if (std::fclose(out) != 0)
      throw RepackError(4, "flush failed for init segment");
  }

  // 5. Fragments in manifest order, mfhd rewritten to 1..N.
  json manifest = json::object();
  manifest["init_segment"] = init_name.string();
  json farr = json::array();
  uint32_t new_seq = 1;
  for (const FragmentInfo &f : idx.fragments) {
    res.mfhd_before.push_back(f.mfhd_sequence);
    fs::path src(f.abs_path);
    fs::path dst = tmp / src.filename();
    std::FILE *in = std::fopen(f.abs_path.c_str(), "rb");
    if (!in) throw Fmp4Error("cannot open fragment: " + f.abs_path);
    std::FILE *out = open_wb(dst);
    // Copy [0, mfhd value field) and (value field end, EOF); patch 4 bytes.
    uint64_t value_off = f.mfhd_offset + f.mfhd_header_size + 4;
    uint64_t fsize = fs::file_size(f.abs_path, ec);
    stream_copy_range(in, out, 0, value_off);
    uint8_t be[4] = {uint8_t(new_seq >> 24), uint8_t(new_seq >> 16),
                     uint8_t(new_seq >> 8), uint8_t(new_seq)};
    if (std::fwrite(be, 1, 4, out) != 4)
      throw RepackError(4, "mfhd rewrite write failed");
    stream_copy_range(in, out, value_off + 4, fsize);
    std::fclose(in);
    if (std::fclose(out) != 0)
      throw RepackError(4, "flush failed for fragment " + f.rel_path);
    res.mfhd_after.push_back(new_seq);
    farr.push_back({{"sequence", new_seq},
                    {"path", src.filename().string()}});
    ++new_seq;
  }
  res.output_fragment_count = new_seq - 1;

  // 6. Rewrite the manifest for the new contiguous sequences.
  manifest["fragments"] = std::move(farr);
  {
    std::ofstream mos(tmp / "fragments.manifest",
                      std::ios::binary | std::ios::trunc);
    if (!mos) throw RepackError(4, "cannot write manifest");
    mos << manifest.dump(2);
  }

  // 7. Re-index and re-validate the temp output against the baseline.
  FragmentIndex out_idx = build_index(tmp.string());
  res.verification = validate_against_baseline(out_idx, idx);
  if (!res.verification.ok()) {
    std::string detail = "repack verification failed";
    if (!res.verification.issues.empty())
      detail += ": " + res.verification.issues.front().detail;
    throw RepackError(3, detail);
  }

  // 8. Atomic commit: rename temp -> final.
  std::error_code rename_ec;
  fs::rename(tmp, out_p, rename_ec);
  if (rename_ec)
    throw RepackError(4, "atomic rename failed: " + rename_ec.message());
  guard.committed = true;
  res.output_dir = out_parent;
  return res;
}

} // namespace fmp4
