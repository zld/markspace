#include "fmp4/fixture.hpp"

#include <cstdio>
#include <filesystem>
#include <vector>

#include "fmp4/box.hpp"

namespace fmp4 {
namespace fs = std::filesystem;
namespace {

void write_trex(BoxWriter& w, uint32_t track_id, uint32_t dur, uint32_t size) {
  size_t t = w.begin_full_box("trex", 0, 0);
  w.u32(track_id);
  w.u32(1);  // default_sample_description_index
  w.u32(dur);
  w.u32(size);
  w.u32(0);
  w.end_box(t);
}

void write_stbl(BoxWriter& w, const char* sample_type) {
  size_t stbl = w.begin_box("stbl");
  size_t stsd = w.begin_full_box("stsd", 0, 0);
  w.u32(1);  // entry_count
  size_t entry = w.begin_box(sample_type);
  w.zeros(6);
  w.u16(1);  // data_reference_index
  w.zeros(70);
  w.end_box(entry);
  w.end_box(stsd);
  for (const char* t : {"stts", "stsc", "stco"}) {
    size_t b = w.begin_full_box(t, 0, 0);
    w.u32(0);
    w.end_box(b);
  }
  size_t stsz = w.begin_full_box("stsz", 0, 0);
  w.u32(0);
  w.u32(0);
  w.end_box(stsz);
  w.end_box(stbl);
}

void write_trak(BoxWriter& w, uint32_t track_id, uint32_t timescale,
                bool video) {
  size_t trak = w.begin_box("trak");
  size_t tkhd = w.begin_full_box("tkhd", 0, 0x7);
  w.u32(0);
  w.u32(0);
  w.u32(track_id);
  w.u32(0);
  w.u32(0);    // duration
  w.zeros(8);  // reserved
  w.u16(0);
  w.u16(0);
  w.u16(0);
  w.u16(0);
  for (uint32_t m : {0x10000u, 0u, 0u, 0u, 0x10000u, 0u, 0u, 0u, 0x40000000u})
    w.u32(m);
  w.u32(0);
  w.u32(0);  // width, height
  w.end_box(tkhd);
  size_t mdia = w.begin_box("mdia");
  size_t mdhd = w.begin_full_box("mdhd", 0, 0);
  w.u32(0);
  w.u32(0);
  w.u32(timescale);
  w.u32(0);
  w.u16(0x55c4);  // und language
  w.u16(0);
  w.end_box(mdhd);
  size_t hdlr = w.begin_full_box("hdlr", 0, 0);
  w.u32(0);
  w.fourcc(video ? "vide" : "soun");
  w.zeros(12);
  w.u8(0);
  w.end_box(hdlr);
  size_t minf = w.begin_box("minf");
  size_t mh = w.begin_full_box(video ? "vmhd" : "smhd", 0, video ? 1 : 0);
  w.u16(0);
  w.u16(0);
  if (video) {
    w.u16(0);
    w.u16(0);
  }
  w.end_box(mh);
  size_t dinf = w.begin_box("dinf");
  size_t dref = w.begin_full_box("dref", 0, 0);
  w.u32(1);
  size_t url = w.begin_full_box("url ", 0, 1);
  w.end_box(url);
  w.end_box(dref);
  w.end_box(dinf);
  write_stbl(w, video ? "avc1" : "mp4a");
  w.end_box(minf);
  w.end_box(mdia);
  w.end_box(trak);
}

void write_init(const std::string& path, bool bad_timescale) {
  BoxWriter w;
  size_t ftyp = w.begin_box("ftyp");
  w.fourcc("isom");
  w.u32(0x200);
  w.fourcc("isom");
  w.fourcc("iso6");
  w.fourcc("cmfc");
  w.end_box(ftyp);
  size_t moov = w.begin_box("moov");
  size_t mvhd = w.begin_full_box("mvhd", 0, 0);
  w.u32(0);
  w.u32(0);
  w.u32(1000);
  w.u32(0);
  w.u32(0x10000);
  w.u16(0x100);
  w.u16(0);
  w.zeros(8);
  for (uint32_t m : {0x10000u, 0u, 0u, 0u, 0x10000u, 0u, 0u, 0u, 0x40000000u})
    w.u32(m);
  w.zeros(24);
  w.u32(3);  // next_track_id
  w.end_box(mvhd);
  write_trak(w, 1, 90000, true);
  write_trak(w, 2, bad_timescale ? 0 : 48000, false);
  size_t mvex = w.begin_box("mvex");
  write_trex(w, 1, 3000, 100);
  write_trex(w, 2, 1024, 80);
  w.end_box(mvex);
  w.end_box(moov);
  write_file(path, w.buf);
}

struct SegSpec {
  uint32_t seq;
  uint8_t tfdt_version;
  uint8_t trun_version;
  int duration_mode;  // 0 = trun, 1 = tfhd default, 2 = trex default
  int64_t tfdt1;
  int64_t tfdt2;
};

void write_traf(BoxWriter& w, uint32_t track_id, uint32_t sample_count,
                uint32_t sample_dur, uint32_t sample_size,
                const SegSpec& spec, int64_t tfdt,
                std::vector<size_t>& data_offset_positions) {
  size_t traf = w.begin_box("traf");
  uint32_t tfhd_flags = 0x20000 | 0x2;  // default-base-is-moof | sample desc idx
  if (spec.duration_mode == 1) tfhd_flags |= 0x8;
  size_t tfhd = w.begin_full_box("tfhd", 0, tfhd_flags);
  w.u32(track_id);
  w.u32(1);  // sample_description_index
  if (spec.duration_mode == 1) w.u32(sample_dur);
  w.end_box(tfhd);
  size_t tfdt_box = w.begin_full_box("tfdt", spec.tfdt_version, 0);
  if (spec.tfdt_version == 1)
    w.u64((uint64_t)tfdt);
  else
    w.u32((uint32_t)tfdt);
  w.end_box(tfdt_box);
  uint32_t trun_flags = 0x1 | 0x200 | 0x800;  // data-offset, size, cto
  if (spec.duration_mode == 0) trun_flags |= 0x100;
  size_t trun = w.begin_full_box("trun", spec.trun_version, trun_flags);
  w.u32(sample_count);
  data_offset_positions.push_back(w.buf.size());
  w.i32(0);  // data_offset placeholder, patched after moof size is known
  for (uint32_t i = 0; i < sample_count; ++i) {
    if (spec.duration_mode == 0) w.u32(sample_dur);
    w.u32(sample_size);
    if (spec.trun_version == 1)
      w.i32(i == 0 ? -2 : 2);  // signed cto
    else
      w.u32(2);  // unsigned cto
  }
  w.end_box(trun);
  w.end_box(traf);
}

void write_segment(const std::string& path, const SegSpec& spec) {
  BoxWriter w;
  size_t moof = w.begin_box("moof");
  size_t mfhd = w.begin_full_box("mfhd", 0, 0);
  w.u32(spec.seq);
  w.end_box(mfhd);
  std::vector<size_t> patches;
  write_traf(w, 1, 3, 3000, 100, spec, spec.tfdt1, patches);
  write_traf(w, 2, 4, 1024, 80, spec, spec.tfdt2, patches);
  w.end_box(moof);
  uint64_t moof_size = w.buf.size();
  std::vector<uint8_t> payload(3 * 100 + 4 * 80);
  for (size_t i = 0; i < payload.size(); ++i)
    payload[i] = (uint8_t)(spec.seq + i);
  size_t mdat = w.begin_box("mdat");
  w.bytes(payload.data(), payload.size());
  w.end_box(mdat);
  uint32_t off1 = (uint32_t)moof_size + 8;  // moof-relative, 8-byte mdat header
  w.patch_i32(patches[0], (int32_t)off1);
  w.patch_i32(patches[1], (int32_t)(off1 + 300));
  write_file(path, w.buf);
}

std::string seg_path(const std::string& dir, uint32_t seq) {
  char name[64];
  std::snprintf(name, sizeof(name), "seg-%04u.mp4", seq);
  return dir + "/" + name;
}

void write_segment_set(const std::string& dir, bool skip_43, bool delta) {
  std::error_code ec;
  fs::create_directories(dir, ec);
  if (ec) throw Fmp4Error(kExitUsage, "cannot create dir: " + dir);
  const SegSpec specs[4] = {
      {41, 0, 0, 0, 0, 0},
      {42, 1, 1, 0, 9000, 4096},
      {43, 0, 1, 1, 18000, 8192},
      {44, 1, 0, 2, 27000, 12288},
  };
  for (const auto& s : specs) {
    if (skip_43 && s.seq == 43) continue;
    SegSpec spec = s;
    if (delta && s.seq == 42) spec.tfdt1 += 500;  // +500/-500 tick jump
    write_segment(seg_path(dir, s.seq), spec);
  }
}

}  // namespace

void write_fixture(const std::string& out_dir) {
  std::error_code ec;
  fs::create_directories(out_dir, ec);
  if (ec) throw Fmp4Error(kExitUsage, "cannot create dir: " + out_dir);
  write_init(out_dir + "/init.mp4", false);
  write_init(out_dir + "/init-bad.mp4", true);
  write_segment_set(out_dir + "/segments-good", false, false);
  write_segment_set(out_dir + "/segments-missing", true, false);
  write_segment_set(out_dir + "/segments-delta", false, true);
  // Corrupt segment: declares a 1000-byte box but is truncated.
  fs::create_directories(out_dir + "/segments-corrupt", ec);
  BoxWriter w;
  w.u32(1000);
  w.fourcc("mdat");
  w.zeros(8);
  write_file(seg_path(out_dir + "/segments-corrupt", 41), w.buf);
}

}  // namespace fmp4
