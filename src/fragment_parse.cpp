#include "fmp4/fragment_index.hpp"

#include <filesystem>

namespace fmp4 {
namespace fs = std::filesystem;

namespace flags {
// tfhd
constexpr uint32_t kTfhdBaseDataOffset = 0x000001;
constexpr uint32_t kTfhdSampleDesc = 0x000002;
constexpr uint32_t kTfhdDefaultDuration = 0x000008;
constexpr uint32_t kTfhdDefaultSize = 0x000010;
constexpr uint32_t kTfhdDefaultFlags = 0x000020;
constexpr uint32_t kTfhdDurationIsEmpty = 0x010000;
constexpr uint32_t kTfhdDefaultBaseIsMoof = 0x020000;
// trun
constexpr uint32_t kTrunDataOffset = 0x000001;
constexpr uint32_t kTrunFirstFlags = 0x000004;
constexpr uint32_t kTrunHasDuration = 0x000100;
constexpr uint32_t kTrunHasSize = 0x000200;
constexpr uint32_t kTrunHasFlags = 0x000400;
constexpr uint32_t kTrunHasCto = 0x000800;
} // namespace flags

struct TrafDecoded {
  uint32_t track_id = 0;
  bool has_tfdt = false;
  uint64_t decode_time = 0;
  uint32_t sample_count = 0;
  uint64_t duration_sum = 0;
};

static uint32_t be32_at(const std::vector<uint8_t> &p, size_t i) {
  return (uint32_t(p[i]) << 24) | (uint32_t(p[i + 1]) << 16) |
         (uint32_t(p[i + 2]) << 8) | uint32_t(p[i + 3]);
}
static uint64_t be64_at(const std::vector<uint8_t> &p, size_t i) {
  uint64_t v = 0;
  for (int k = 0; k < 8; ++k) v = (v << 8) | p[i + k];
  return v;
}
static uint8_t fb_version(const std::vector<uint8_t> &p) { return p[0]; }
static uint32_t fb_flags(const std::vector<uint8_t> &p) {
  return be32_at(p, 0) & 0x00ffffff;
}

struct TfhdDefaults {
  uint32_t duration = 0, size = 0, flags = 0, desc_idx = 1;
  bool duration_present = false;
};

static TfhdDefaults parse_tfhd(const std::vector<uint8_t> &p,
                               const InitInfo &init, uint64_t box_off,
                               TrafDecoded &t) {
  if (p.size() < 8) throw Fmp4Error("tfhd too short", init.path, box_off);
  uint32_t fl = fb_flags(p);
  t.track_id = be32_at(p, 4);
  const TrackInit *ti = nullptr;
  for (const auto &tr : init.tracks)
    if (tr.track_id == t.track_id) ti = &tr;
  if (!ti)
    throw Fmp4Error("tfhd references unknown track id " +
                        std::to_string(t.track_id),
                    init.path, box_off);
  // Resolution order per ISO 14496-12: trun -> tfhd -> trex.
  TfhdDefaults d;
  d.duration = ti->default_sample_duration;
  d.size = ti->default_sample_size;
  d.flags = ti->default_sample_flags;
  d.desc_idx = ti->default_sample_desc_idx;
  size_t pos = 8;
  auto take32 = [&](const char *what) {
    if (pos + 4 > p.size())
      throw Fmp4Error(std::string("truncated tfhd ") + what, init.path,
                      box_off + pos);
    uint32_t v = be32_at(p, pos);
    pos += 4;
    return v;
  };
  if (fl & flags::kTfhdBaseDataOffset) {
    if (pos + 8 > p.size())
      throw Fmp4Error("truncated tfhd base-data-offset", init.path, box_off);
    pos += 8;
  }
  if (fl & flags::kTfhdSampleDesc) d.desc_idx = take32("sample-desc-index");
  if (fl & flags::kTfhdDefaultDuration) {
    d.duration = take32("default-sample-duration");
    d.duration_present = true;
  }
  if (fl & flags::kTfhdDefaultSize) d.size = take32("default-sample-size");
  if (fl & flags::kTfhdDefaultFlags) d.flags = take32("default-sample-flags");
  (void)flags::kTfhdDurationIsEmpty;
  (void)flags::kTfhdDefaultBaseIsMoof;
  return d;
}

static void parse_tfdt(const std::vector<uint8_t> &p, uint64_t box_off,
                       const std::string &file, TrafDecoded &t) {
  if (p.size() < 8) throw Fmp4Error("tfdt too short", file, box_off);
  uint8_t ver = fb_version(p);
  if (ver == 1) {
    if (p.size() < 12) throw Fmp4Error("tfdt v1 truncated", file, box_off);
    t.decode_time = be64_at(p, 4);
  } else if (ver == 0) {
    t.decode_time = be32_at(p, 4);
  } else {
    throw Fmp4Error("unsupported tfdt version " + std::to_string(ver), file,
                    box_off);
  }
  t.has_tfdt = true;
}

// Streams trun sample fields without materializing samples. v1 uses signed
// 32-bit composition-time offsets; they are validated, not applied, since the
// index tracks decode timelines.
static void parse_trun(const std::vector<uint8_t> &p, uint64_t box_off,
                       const std::string &file, const TfhdDefaults &tfhd,
                       const TrackInit &ti, TrafDecoded &t) {
  if (p.size() < 8) throw Fmp4Error("trun too short", file, box_off);
  uint8_t ver = fb_version(p);
  if (ver > 1)
    throw Fmp4Error("unsupported trun version " + std::to_string(ver), file,
                    box_off);
  uint32_t fl = fb_flags(p);
  uint32_t count = be32_at(p, 4);
  size_t pos = 8;
  if (fl & flags::kTrunDataOffset) pos += 4;
  if (fl & flags::kTrunFirstFlags) pos += 4;
  const bool has_dur = (fl & flags::kTrunHasDuration) != 0;
  const bool has_size = (fl & flags::kTrunHasSize) != 0;
  const bool has_sflags = (fl & flags::kTrunHasFlags) != 0;
  const bool has_cto = (fl & flags::kTrunHasCto) != 0;
  unsigned per = (has_dur ? 1u : 0) + (has_size ? 1u : 0) +
                 (has_sflags ? 1u : 0) + (has_cto ? 1u : 0);
  uint64_t need = uint64_t(pos) + uint64_t(count) * uint64_t(per) * 4u;
  if (need > p.size())
    throw Fmp4Error("trun sample table truncated", file, box_off);
  const uint32_t fallback =
      tfhd.duration_present ? tfhd.duration : ti.default_sample_duration;
  for (uint32_t i = 0; i < count; ++i) {
    uint32_t dur = fallback;
    if (has_dur) {
      dur = be32_at(p, pos);
      pos += 4;
    }
    if (has_size) pos += 4;
    if (has_sflags) pos += 4;
    if (has_cto) {
      int32_t cto = static_cast<int32_t>(be32_at(p, pos));
      pos += 4;
      // v0 cto must be unsigned/non-negative per 14496-12.
      if (ver == 0 && cto < 0)
        throw Fmp4Error("negative composition offset in trun v0", file,
                        box_off);
    }
    t.duration_sum += dur;
  }
  t.sample_count += count;
}

static void parse_traf(BoxReader &moof_r, const BoxHeader &traf_box,
                       const InitInfo &init, const std::string &file,
                       FragmentInfo &out) {
  BoxReader r = moof_r.enter(traf_box, 3);
  BoxHeader b;
  TrafDecoded t;
  TfhdDefaults tfhd{};
  const TrackInit *ti = nullptr;
  while (r.next(b)) {
    auto payload = r.read_payload(b);
    if (b.type == FOURCC("tfhd")) {
      tfhd = parse_tfhd(payload, init, b.offset, t);
      for (const auto &tr : init.tracks)
        if (tr.track_id == t.track_id) ti = &tr;
    } else if (b.type == FOURCC("tfdt")) {
      parse_tfdt(payload, b.offset, file, t);
    } else if (b.type == FOURCC("trun")) {
      if (!ti) throw Fmp4Error("trun before tfhd in traf", file, b.offset);
      parse_trun(payload, b.offset, file, tfhd, *ti, t);
    }
    // unknown traf children are skipped by the cursor
  }
  if (t.track_id == 0)
    throw Fmp4Error("traf missing tfhd", file, traf_box.offset);
  TrackRun run;
  run.track_id = t.track_id;
  run.decode_start = t.has_tfdt ? t.decode_time : 0;
  run.duration = t.duration_sum;
  run.sample_count = t.sample_count;
  out.tracks.push_back(run);
}

FragmentInfo parse_fragment_file(const std::string &abs_path,
                                 const InitInfo &init) {
  FragmentInfo out;
  out.abs_path = abs_path;
  FileSource src(abs_path);
  out.file_size = src.size();
  BoxReader top(src, 0, src.size());
  BoxHeader b;
  bool have_moof = false;
  while (top.next(b)) {
    if (b.type != FOURCC("moof")) continue; // ftyp/mdat/unknown kept on disk
    if (have_moof)
      throw Fmp4Error("fragment contains multiple moof boxes", abs_path,
                      b.offset);
    have_moof = true;
    out.moof_offset = b.offset;
    BoxReader moof = top.enter(b, 2);
    BoxHeader mb;
    while (moof.next(mb)) {
      if (mb.type == FOURCC("mfhd")) {
        if (mb.payload_size < 8)
          throw Fmp4Error("mfhd too short", abs_path, mb.offset);
        out.mfhd_sequence = moof.read_u32(mb, 4);
        out.mfhd_offset = mb.offset;
        out.mfhd_header_size = uint32_t(mb.header_size);
      } else if (mb.type == FOURCC("traf")) {
        parse_traf(moof, mb, init, abs_path, out);
      }
    }
  }
  if (!have_moof)
    throw Fmp4Error("fragment has no moof box", abs_path, 0);
  if (out.mfhd_header_size == 0)
    throw Fmp4Error("moof missing mfhd", abs_path, out.moof_offset);
  return out;
}

} // namespace fmp4
