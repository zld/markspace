#include "fmp4/fixture_writer.hpp"

#include <filesystem>
#include <fstream>
#include <nlohmann/json.hpp>

namespace fmp4::fixture {

using nlohmann::json;
namespace fs = std::filesystem;

namespace {

void put32(std::vector<uint8_t> &v, uint32_t x) {
  v.push_back(uint8_t(x >> 24)); v.push_back(uint8_t(x >> 16));
  v.push_back(uint8_t(x >> 8));  v.push_back(uint8_t(x));
}
void put64(std::vector<uint8_t> &v, uint64_t x) {
  for (int i = 7; i >= 0; --i) v.push_back(uint8_t(x >> (8 * i)));
}
void putcc(std::vector<uint8_t> &v, const char *s) {
  for (int i = 0; i < 4; ++i) v.push_back(uint8_t(s[i]));
}
void append(std::vector<uint8_t> &dst, const std::vector<uint8_t> &src) {
  dst.insert(dst.end(), src.begin(), src.end());
}
std::vector<uint8_t> box(const char *type,
                         const std::vector<uint8_t> &payload) {
  std::vector<uint8_t> b;
  put32(b, uint32_t(8 + payload.size()));
  putcc(b, type);
  append(b, payload);
  return b;
}
std::vector<uint8_t> fullbox(const char *type, uint8_t ver, uint32_t flags,
                             const std::vector<uint8_t> &rest) {
  std::vector<uint8_t> p = {ver, uint8_t(flags >> 16),
                            uint8_t(flags >> 8), uint8_t(flags)};
  append(p, rest);
  return box(type, p);
}

constexpr uint32_t kVideoScale = 90000;
constexpr uint32_t kAudioScale = 48000;
constexpr uint32_t kVideoDur = 3000;   // 1/30 s
constexpr uint32_t kAudioDur = 1024;

std::vector<uint8_t> mdhd(uint32_t scale) {
  std::vector<uint8_t> p(20, 0);
  p[12] = uint8_t(scale >> 24); p[13] = uint8_t(scale >> 16);
  p[14] = uint8_t(scale >> 8);  p[15] = uint8_t(scale);
  return box("mdhd", p);
}
std::vector<uint8_t> hdlr(const char *kind) {
  std::vector<uint8_t> p(12, 0);
  p[8] = kind[0]; p[9] = kind[1]; p[10] = kind[2]; p[11] = kind[3];
  return box("hdlr", p);
}
std::vector<uint8_t> stsd(const char *entry) {
  std::vector<uint8_t> p;
  put32(p, 0); put32(p, 1);
  append(p, box(entry, std::vector<uint8_t>(8, 0)));
  return box("stsd", p);
}
std::vector<uint8_t> tkhd(uint32_t id) {
  std::vector<uint8_t> p(20, 0);
  p[12] = uint8_t(id >> 24); p[13] = uint8_t(id >> 16);
  p[14] = uint8_t(id >> 8);  p[15] = uint8_t(id);
  return box("tkhd", p);
}
std::vector<uint8_t> trex(uint32_t id, uint32_t dur) {
  std::vector<uint8_t> p;
  put32(p, 0); put32(p, id); put32(p, 1); put32(p, dur);
  p.resize(p.size() + 8);
  return box("trex", p);
}
std::vector<uint8_t> trak(uint32_t id, uint32_t scale, const char *handler,
                          const char *entry) {
  std::vector<uint8_t> mdia;
  append(mdia, mdhd(scale));
  append(mdia, hdlr(handler));
  append(mdia, box("minf", box("stbl", stsd(entry))));
  std::vector<uint8_t> t;
  append(t, tkhd(id));
  append(t, box("mdia", mdia));
  return box("trak", t);
}
std::vector<uint8_t> mfhd(uint32_t seq) {
  std::vector<uint8_t> p; put32(p, 0); put32(p, seq);
  return box("mfhd", p);
}

// One traf per track; timelines are contiguous so validation yields zero
// deltas before and after repacking.
std::vector<uint8_t> traf(uint32_t track_id, uint32_t index, uint32_t dur,
                          uint32_t scale, bool mixed) {
  bool v1 = mixed && ((index & 1u) == 0); // alternate per segment
  std::vector<uint8_t> tfhd; put32(tfhd, track_id);
  std::vector<uint8_t> out;
  append(out, fullbox("tfhd", 0, 0, tfhd));

  uint64_t decode_time = uint64_t(index) * dur;
  std::vector<uint8_t> tfdt;
  if (v1) put64(tfdt, decode_time);
  else put32(tfdt, uint32_t(decode_time));
  append(out, fullbox("tfdt", v1 ? 1 : 0, 0, tfdt));

  std::vector<uint8_t> trun;
  put32(trun, 1);           // one sample
  put32(trun, dur);         // sample duration
  if (v1 && track_id == 1) { // trun v1 signed composition offset on video
    put32(trun, uint32_t(100)); // sample size
    put32(trun, uint32_t(0));   // cto == 0, but exercises the v1 field
    append(out, fullbox("trun", 1, 0x000100 | 0x000200 | 0x000800, trun));
  } else {
    append(out, fullbox("trun", 0, 0x000100, trun));
  }
  (void)scale;
  return box("traf", out);
}

void write_bytes(const fs::path &p, const std::vector<uint8_t> &b) {
  std::ofstream os(p, std::ios::binary);
  os.write(reinterpret_cast<const char *>(b.data()),
           std::streamsize(b.size()));
}

} // namespace

std::vector<uint32_t> write_fixture(const Options &opt) {
  fs::path root(opt.dir);
  std::error_code ec;
  fs::create_directories(root, ec);

  // Init segment with two tracks.
  std::vector<uint8_t> moov;
  const char *video_entry = opt.incompatible_init ? "encv" : "avc1";
  append(moov, trak(1, kVideoScale, "vide", video_entry));
  append(moov, trak(2, kAudioScale, "soun", "mp4a"));
  std::vector<uint8_t> mvex;
  append(mvex, trex(1, kVideoDur));
  append(mvex, trex(2, kAudioDur));
  append(moov, box("mvex", mvex));
  std::vector<uint8_t> init;
  append(init, box("ftyp", std::vector<uint8_t>(8, 0)));
  append(init, box("moov", moov));
  write_bytes(root / "init.mp4", init);

  json farr = json::array();
  std::vector<uint32_t> mfhd_seqs;
  uint32_t next_mfhd = 101;
  for (uint32_t i = 1; i <= opt.segments; ++i) {
    uint32_t mseq = next_mfhd++;
    if (opt.mfhd_gap_at == i) mseq = next_mfhd++; // skip one number
    mfhd_seqs.push_back(mseq);
    std::string rel = "frag-" + std::to_string(i) + ".m4s";
    farr.push_back({{"sequence", i}, {"path", rel}});
    if (opt.omit_file_for_seq == i) continue;

    std::vector<uint8_t> moof;
    append(moof, mfhd(mseq));
    append(moof, traf(1, i - 1, kVideoDur, kVideoScale,
                      opt.mixed_versions));
    append(moof, traf(2, i - 1, kAudioDur, kAudioScale,
                      opt.mixed_versions));
    std::vector<uint8_t> file;
    append(file, box("moof", moof));
    // Small placeholder mdat (synthetic padding only; never decoded).
    append(file, box("mdat", std::vector<uint8_t>(32, uint8_t(0x7f))));
    if (opt.corrupt_length_at == i && file.size() > 8)
      file[0] = 0xff; // implausible size extending past EOF
    write_bytes(root / rel, file);
  }
  json manifest = {{"init_segment", "init.mp4"},
                   {"fragments", std::move(farr)}};
  std::ofstream mos(root / "fragments.manifest", std::ios::binary);
  mos << manifest.dump(2);
  return mfhd_seqs;
}

} // namespace fmp4::fixture
