#include "fmp4/box_reader.hpp"
#include "fmp4/errors.hpp"
#include "fmp4/fragment_index.hpp"

#include <filesystem>
#include <fstream>
#include <nlohmann/json.hpp>
#include <system_error>

#include "fmp4/safe_path.hpp"

namespace fmp4 {

using nlohmann::json;
namespace fs = std::filesystem;

namespace cc {
constexpr uint32_t kMoov = FOURCC("moov");
constexpr uint32_t kTrak = FOURCC("trak");
constexpr uint32_t kMdia = FOURCC("mdia");
constexpr uint32_t kMdhd = FOURCC("mdhd");
constexpr uint32_t kHdlr = FOURCC("hdlr");
constexpr uint32_t kMinf = FOURCC("minf");
constexpr uint32_t kStbl = FOURCC("stbl");
constexpr uint32_t kStsd = FOURCC("stsd");
constexpr uint32_t kMvex = FOURCC("mvex");
constexpr uint32_t kTrex = FOURCC("trex");
constexpr uint32_t kTkhd = FOURCC("tkhd");
[[maybe_unused]] constexpr uint32_t kMoof = FOURCC("moof");
[[maybe_unused]] constexpr uint32_t kMfhd = FOURCC("mfhd");
[[maybe_unused]] constexpr uint32_t kTraf = FOURCC("traf");
[[maybe_unused]] constexpr uint32_t kTfhd = FOURCC("tfhd");
[[maybe_unused]] constexpr uint32_t kTfdt = FOURCC("tfdt");
[[maybe_unused]] constexpr uint32_t kTrun = FOURCC("trun");
[[maybe_unused]] constexpr uint32_t kMdat = FOURCC("mdat");
} // namespace cc

std::string manifest_path(const std::string &dir) {
  return (fs::path(dir) / "fragments.manifest").string();
}

const FragmentInfo *FragmentIndex::find_manifest(uint32_t seq) const {
  for (const auto &f : fragments)
    if (f.manifest_seq == seq) return &f;
  return nullptr;
}

const TrackInit *FragmentIndex::find_track(uint32_t track_id) const {
  for (const auto &t : init.tracks)
    if (t.track_id == track_id) return &t;
  return nullptr;
}

// ---- init segment ---------------------------------------------------------

static uint32_t parse_mdhd_timescale(BoxReader &r, const BoxHeader &mdhd) {
  uint8_t version = r.read_u8(mdhd, 0);
  // v0: version(1)+flags(3)+ct(4)+mtime(4)+timescale@12
  // v1: version(1)+flags(3)+ct(8)+mtime(8)+timescale@20
  return r.read_u32(mdhd, version == 1 ? 20 : 12);
}

static uint32_t parse_hdlr_type(BoxReader &r, const BoxHeader &hdlr) {
  return r.read_u32(hdlr, 8); // version/flags(4) + pre_defined(4)
}

static uint32_t parse_stsd_entry(BoxReader &r, const BoxHeader &stsd) {
  uint32_t entry_count = r.read_u32(stsd, 4); // after version/flags
  if (entry_count == 0)
    throw Fmp4Error("stsd contains no sample entries", r.source().name(),
                    stsd.offset);
  // layout: version/flags(4) entry_count(4), then sample entry boxes each
  // starting with size(4)+type(4); the entry's own reserved[6]+data_index
  // live inside its payload.
  uint8_t four[4];
  r.read_abs(stsd.payload_offset + 12, four, 4);
  return (uint32_t(four[0]) << 24) | (uint32_t(four[1]) << 16) |
         (uint32_t(four[2]) << 8) | uint32_t(four[3]);
}

static void parse_trak(BoxReader &moov_r, const BoxHeader &trak,
                       TrackInit &out) {
  BoxReader r = moov_r.enter(trak, 2);
  BoxHeader b;
  while (r.next(b)) {
    if (b.type == cc::kTkhd) {
      uint8_t version = r.read_u8(b, 0);
      // v0 track_id@12, v1 track_id@20 (after two 64-bit times)
      out.track_id = r.read_u32(b, version == 1 ? 20 : 12);
    } else if (b.type == cc::kMdia) {
      BoxReader mdia = r.enter(b, 3);
      BoxHeader mb;
      while (mdia.next(mb)) {
        if (mb.type == cc::kMdhd)
          out.timescale = parse_mdhd_timescale(mdia, mb);
        else if (mb.type == cc::kHdlr)
          out.handler = parse_hdlr_type(mdia, mb);
        else if (mb.type == cc::kMinf) {
          BoxReader minf = mdia.enter(mb, 4);
          BoxHeader ib;
          while (minf.next(ib)) {
            if (ib.type != cc::kStbl) continue;
            BoxReader stbl = minf.enter(ib, 5);
            BoxHeader sb;
            while (stbl.next(sb))
              if (sb.type == cc::kStsd)
                out.sample_entry = parse_stsd_entry(stbl, sb);
          }
        }
      }
    }
  }
  if (out.track_id == 0 || out.timescale == 0)
    throw Fmp4Error("trak missing tkhd track_id or mdhd timescale",
                    r.source().name(), trak.offset);
}

static void parse_trex(BoxReader &r, const BoxHeader &trex, TrackInit &t) {
  t.track_id = r.read_u32(trex, 4);
  t.default_sample_desc_idx = r.read_u32(trex, 8);
  t.default_sample_duration = r.read_u32(trex, 12);
  t.default_sample_size = r.read_u32(trex, 16);
  t.default_sample_flags = r.read_u32(trex, 20);
}

InitInfo parse_init_segment(const std::string &abs_path) {
  InitInfo info;
  info.path = abs_path;
  FileSource src(abs_path);
  BoxReader top(src, 0, src.size());
  BoxHeader b;
  while (top.next(b)) {
    if (b.type != cc::kMoov) continue;
    BoxReader moov = top.enter(b, 1);
    BoxHeader mb;
    while (moov.next(mb)) {
      if (mb.type == cc::kTrak) {
        TrackInit t;
        parse_trak(moov, mb, t);
        info.tracks.push_back(t);
      } else if (mb.type == cc::kMvex) {
        BoxReader mvex = moov.enter(mb, 2);
        BoxHeader xb;
        while (mvex.next(xb)) {
          if (xb.type != cc::kTrex) continue;
          TrackInit *slot = nullptr;
          uint32_t id = mvex.read_u32(xb, 4);
          for (auto &t : info.tracks)
            if (t.track_id == id) slot = &t;
          if (!slot)
            throw Fmp4Error("trex references unknown track id",
                            src.name(), xb.offset);
          parse_trex(mvex, xb, *slot);
        }
      }
    }
  }
  if (info.tracks.empty())
    throw Fmp4Error("init segment has no moov/trak boxes", abs_path, 0);
  return info;
}

// Fragment parsing (fragment_parse.cpp).
FragmentInfo parse_fragment_file(const std::string &abs_path,
                                 const InitInfo &init);

FragmentIndex build_index(const std::string &dir) {
  FragmentIndex idx;
  idx.root_dir = path::canonical_existing(dir);
  std::string mp = manifest_path(idx.root_dir);
  std::ifstream is(mp, std::ios::binary);
  if (!is) throw Fmp4Error("manifest not found: " + mp, mp, 0);
  json j;
  try {
    is >> j;
  } catch (const std::exception &e) {
    throw Fmp4Error(std::string("invalid manifest JSON: ") + e.what(), mp, 0);
  }
  if (!j.is_object() || !j.contains("fragments") ||
      !j["fragments"].is_array())
    throw Fmp4Error("manifest missing 'fragments' array", mp, 0);
  std::string init_rel = j.value("init_segment", "init.mp4");
  try {
    idx.init =
        parse_init_segment(path::join_under(idx.root_dir, init_rel));
  } catch (const Fmp4Error &e) {
    throw StructuralError(e);
  }

  for (const auto &el : j["fragments"]) {
    if (!el.is_object() || !el.contains("sequence") ||
        !el.contains("path"))
      throw Fmp4Error("manifest entry needs sequence and path", mp, 0);
    FragmentInfo fi;
    fi.manifest_seq = el["sequence"].get<uint32_t>();
    fi.rel_path = el["path"].get<std::string>();
    fi.abs_path = path::join_under(idx.root_dir, fi.rel_path);
    if (idx.find_manifest(fi.manifest_seq))
      idx.duplicate_sequences.push_back(fi.manifest_seq);
    std::error_code ec;
    if (fs::exists(fi.abs_path, ec) && !ec) {
      fi.present = true;
      try {
        FragmentInfo parsed = parse_fragment_file(fi.abs_path, idx.init);
        fi.mfhd_sequence = parsed.mfhd_sequence;
        fi.file_size = parsed.file_size;
        fi.moof_offset = parsed.moof_offset;
        fi.mfhd_offset = parsed.mfhd_offset;
        fi.mfhd_header_size = parsed.mfhd_header_size;
        fi.tracks = std::move(parsed.tracks);
      } catch (const Fmp4Error &e) {
        throw StructuralError(e);
      }
    } else {
      fi.present = false;
      idx.missing_sequences.push_back(fi.manifest_seq);
    }
    idx.fragments.push_back(std::move(fi));
  }
  return idx;
}

} // namespace fmp4
