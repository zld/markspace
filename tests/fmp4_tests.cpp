// Test harness; parser/index/validator tests are added in later batches.
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <filesystem>
#include <fstream>
#include <functional>
#include <iterator>
#include <vector>

#include "fmp4/box_reader.hpp"
#include "fmp4/fragment_index.hpp"
#include "fmp4/safe_path.hpp"
#include "fmp4/validator.hpp"
#include "fmp4/repacker.hpp"
#include "fmp4/fixture_writer.hpp"

namespace fs = std::filesystem;

static int g_failures = 0;
static std::vector<std::function<void()>> g_tests;

#define FMP4_TEST(name)                                                      \
  static void name();                                                        \
  namespace {                                                                \
  struct Reg_##name {                                                        \
    Reg_##name() { g_tests.push_back(name); }                                \
  } reg_##name;                                                              \
  }                                                                          \
  static void name()

#define FMP4_CHECK(cond)                                                     \
  do {                                                                       \
    if (!(cond)) {                                                           \
      std::printf("FAIL %s:%d %s\n", __FILE__, __LINE__, #cond);             \
      ++g_failures;                                                          \
    }                                                                        \
  } while (0)

int main() {
  for (auto &t : g_tests) {
    try {
      t();
    } catch (const std::exception &e) {
      std::printf("FAIL uncaught exception: %s\n", e.what());
      ++g_failures;
    }
  }
  std::printf("%zu tests, %d failures\n", g_tests.size(), g_failures);
  return g_failures ? 1 : 0;
}

namespace {
void put32(std::vector<uint8_t> &v, uint32_t x) {
  v.push_back(uint8_t(x >> 24));
  v.push_back(uint8_t(x >> 16));
  v.push_back(uint8_t(x >> 8));
  v.push_back(uint8_t(x));
}
void putcc(std::vector<uint8_t> &v, const char *s) {
  for (int i = 0; i < 4; ++i) v.push_back(uint8_t(s[i]));
}
} // namespace

FMP4_TEST(reader_skips_unknown_boxes_32bit) {
  std::vector<uint8_t> buf;
  put32(buf, 12); putcc(buf, "free"); put32(buf, 0xdeadbeef);
  put32(buf, 8);  putcc(buf, "mdat");
  fmp4::MemorySource src(buf, "mem");
  fmp4::BoxReader r(src, 0, buf.size());
  fmp4::BoxHeader b;
  FMP4_CHECK(r.next(b) && b.type == fmp4::FOURCC("free") && b.end() == 12);
  FMP4_CHECK(r.next(b) && b.type == fmp4::FOURCC("mdat") && b.end() == 20);
  FMP4_CHECK(!r.next(b));
}

FMP4_TEST(reader_handles_extended_size) {
  std::vector<uint8_t> buf;
  put32(buf, 1); putcc(buf, "mdat"); // size32 == 1 => extended
  for (int i = 0; i < 8; ++i) buf.push_back(uint8_t(i == 7 ? 20 : 0));
  for (int i = 0; i < 4; ++i) buf.push_back(uint8_t(0xaa)); // 20-byte box
  fmp4::MemorySource src(buf, "mem");
  fmp4::BoxReader r(src, 0, buf.size());
  fmp4::BoxHeader b;
  FMP4_CHECK(r.next(b));
  FMP4_CHECK(b.header_size == 16 && b.payload_size == 4 && b.end() == 20);
}

FMP4_TEST(reader_rejects_truncation_and_boundary_overflow) {
  std::vector<uint8_t> trunc(3); // header cannot fit
  fmp4::MemorySource tsrc(trunc, "trunc");
  fmp4::BoxReader tr(tsrc, 0, trunc.size());
  fmp4::BoxHeader b;
  bool threw = false;
  try { tr.next(b); } catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);

  std::vector<uint8_t> over;
  put32(over, 100); putcc(over, "moof"); // claims 100, file has 8
  fmp4::MemorySource osrc(over, "over");
  fmp4::BoxReader orr(osrc, 0, over.size());
  threw = false;
  try { orr.next(b); } catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
}

FMP4_TEST(reader_enforces_container_nesting) {
  std::vector<uint8_t> inner;
  put32(inner, 8); putcc(inner, "trex");
  std::vector<uint8_t> outer;
  put32(outer, 8 + uint32_t(inner.size())); putcc(outer, "mvex");
  outer.insert(outer.end(), inner.begin(), inner.end());
  fmp4::MemorySource src(outer, "mem");
  fmp4::BoxReader r(src, 0, outer.size());
  fmp4::BoxHeader mvex;
  FMP4_CHECK(r.next(mvex) && mvex.type == fmp4::FOURCC("mvex"));
  fmp4::BoxReader kids = r.enter(mvex, 1);
  fmp4::BoxHeader trex;
  FMP4_CHECK(kids.next(trex) && trex.type == fmp4::FOURCC("trex"));
  FMP4_CHECK(!kids.next(trex));
}

FMP4_TEST(safe_path_rejects_traversal_and_symlink) {
  fs::path root = fs::temp_directory_path() / "fmp4_safepath_test";
  fs::remove_all(root);
  fs::create_directories(root / "sub");
  std::string canon = fmp4::path::canonical_existing(root.string());
  bool threw = false;
  try { fmp4::path::join_under(canon, "../escape.bin"); }
  catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
  threw = false;
  try { fmp4::path::join_under(canon, "/tmp/abs.bin"); }
  catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
  std::string inside = fmp4::path::join_under(canon, "sub/frag.m4s");
  FMP4_CHECK(fmp4::path::is_within(canon, inside));
  fs::create_symlink(canon, root / "link");
  threw = false;
  try { fmp4::path::canonical_existing((root / "link").string()); }
  catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
  fs::remove_all(root);
}

namespace {
[[maybe_unused]] void put16(std::vector<uint8_t> &v, uint16_t x) {
  v.push_back(uint8_t(x >> 8));
  v.push_back(uint8_t(x));
}
[[maybe_unused]] void put64(std::vector<uint8_t> &v, uint64_t x) {
  for (int i = 7; i >= 0; --i) v.push_back(uint8_t(x >> (8 * i)));
}
void put_bytes(std::vector<uint8_t> &v, const std::vector<uint8_t> &b) {
  v.insert(v.end(), b.begin(), b.end());
}
std::vector<uint8_t> box(const char *type,
                         const std::vector<uint8_t> &payload) {
  std::vector<uint8_t> b;
  put32(b, uint32_t(8 + payload.size()));
  putcc(b, type);
  put_bytes(b, payload);
  return b;
}
} // namespace

FMP4_TEST(parse_init_segment_two_tracks) {
  auto mdhd = [](uint32_t scale) {
    std::vector<uint8_t> p(20, 0); // v0, timescale at rel 12
    p[12] = uint8_t(scale >> 24);
    p[13] = uint8_t(scale >> 16);
    p[14] = uint8_t(scale >> 8);
    p[15] = uint8_t(scale);
    return box("mdhd", p);
  };
  auto hdlr = [](const char *kind) {
    std::vector<uint8_t> p(12, 0);
    p[8] = kind[0]; p[9] = kind[1]; p[10] = kind[2]; p[11] = kind[3];
    return box("hdlr", p);
  };
  auto stsd = [](const char *entry) {
    std::vector<uint8_t> p;
    put32(p, 0);             // version/flags
    put32(p, 1);             // entry count
    // sample entry box; its payload starts with reserved[6]+data_index(2)
    put_bytes(p, box(entry, std::vector<uint8_t>(8, 0)));
    return box("stsd", p);
  };
  auto tkhd = [](uint32_t id) {
    std::vector<uint8_t> p(20, 0); // v0, track_id at rel 12
    p[12] = uint8_t(id >> 24);
    p[13] = uint8_t(id >> 16);
    p[14] = uint8_t(id >> 8);
    p[15] = uint8_t(id);
    return box("tkhd", p);
  };
  auto trex = [](uint32_t id, uint32_t dur) {
    std::vector<uint8_t> p(24, 0);
    p[4] = uint8_t(id >> 24); p[5] = uint8_t(id >> 16);
    p[6] = uint8_t(id >> 8);  p[7] = uint8_t(id);
    p[12] = uint8_t(dur >> 24); p[13] = uint8_t(dur >> 16);
    p[14] = uint8_t(dur >> 8);  p[15] = uint8_t(dur);
    return box("trex", p);
  };
  auto trak = [&](uint32_t id, uint32_t scale, const char *handler,
                  const char *entry) {
    std::vector<uint8_t> stbl;
    put_bytes(stbl, stsd(entry));
    std::vector<uint8_t> minf;
    put_bytes(minf, box("stbl", stbl));
    std::vector<uint8_t> mdia;
    put_bytes(mdia, mdhd(scale));
    put_bytes(mdia, hdlr(handler));
    put_bytes(mdia, box("minf", minf));
    std::vector<uint8_t> t;
    put_bytes(t, tkhd(id));
    put_bytes(t, box("mdia", mdia));
    return box("trak", t);
  };
  std::vector<uint8_t> mvex;
  put_bytes(mvex, trex(1, 3000));
  put_bytes(mvex, trex(2, 1024));
  std::vector<uint8_t> moov;
  put_bytes(moov, trak(1, 90000, "vide", "avc1"));
  put_bytes(moov, trak(2, 48000, "soun", "mp4a"));
  put_bytes(moov, box("mvex", mvex));
  std::vector<uint8_t> file;
  put_bytes(file, box("ftyp", std::vector<uint8_t>(8, 0)));
  put_bytes(file, box("moov", moov));

  fs::path d = fs::temp_directory_path() / "fmp4_init_test";
  fs::remove_all(d);
  fs::create_directories(d);
  std::string p = (d / "init.mp4").string();
  std::ofstream os(p, std::ios::binary);
  os.write(reinterpret_cast<const char *>(file.data()),
           std::streamsize(file.size()));
  os.close();

  fmp4::InitInfo init = fmp4::parse_init_segment(p);
  FMP4_CHECK(init.tracks.size() == 2);
  FMP4_CHECK(init.tracks[0].track_id == 1);
  FMP4_CHECK(init.tracks[0].timescale == 90000);
  FMP4_CHECK(init.tracks[0].handler == fmp4::FOURCC("vide"));
  FMP4_CHECK(init.tracks[0].sample_entry == fmp4::FOURCC("avc1"));
  FMP4_CHECK(init.tracks[0].default_sample_duration == 3000);
  FMP4_CHECK(init.tracks[1].track_id == 2);
  FMP4_CHECK(init.tracks[1].timescale == 48000);
  FMP4_CHECK(init.tracks[1].sample_entry == fmp4::FOURCC("mp4a"));
  FMP4_CHECK(init.tracks[1].default_sample_duration == 1024);
  fs::remove_all(d);
}

namespace {
std::vector<uint8_t> fullbox(const char *type, uint8_t ver, uint32_t flags,
                             const std::vector<uint8_t> &rest) {
  std::vector<uint8_t> p;
  p.push_back(ver);
  p.push_back(uint8_t(flags >> 16));
  p.push_back(uint8_t(flags >> 8));
  p.push_back(uint8_t(flags));
  put_bytes(p, rest);
  return box(type, p);
}
void write_file(const fs::path &p, const std::vector<uint8_t> &buf) {
  std::ofstream os(p, std::ios::binary);
  os.write(reinterpret_cast<const char *>(buf.data()),
           std::streamsize(buf.size()));
}
fmp4::InitInfo two_track_init() {
  fmp4::InitInfo init;
  fmp4::TrackInit v; v.track_id = 1; v.timescale = 90000;
  v.handler = fmp4::FOURCC("vide"); v.sample_entry = fmp4::FOURCC("avc1");
  v.default_sample_duration = 3000;
  fmp4::TrackInit a; a.track_id = 2; a.timescale = 48000;
  a.handler = fmp4::FOURCC("soun"); a.sample_entry = fmp4::FOURCC("mp4a");
  a.default_sample_duration = 1024;
  init.tracks = {v, a};
  return init;
}
std::vector<uint8_t> mfhd(uint32_t seq) {
  std::vector<uint8_t> r; put32(r, 0); put32(r, seq);
  return box("mfhd", r);
}
} // namespace

FMP4_TEST(parse_fragment_tfdt_trun_versions_and_fallbacks) {
  fs::path d = fs::temp_directory_path() / "fmp4_frag_test";
  fs::remove_all(d); fs::create_directories(d);
  auto init = two_track_init();

  // Fragment 1: tfdt v0, trun v1 with per-sample duration + signed cto.
  {
    std::vector<uint8_t> tfdt; put32(tfdt, 9000); // v0: u32 value
    std::vector<uint8_t> trun;
    put32(trun, 2);        // sample count
    put32(trun, 3000); put32(trun, 100); put32(trun, 50); // s1 d/sz/cto
    put32(trun, 3000); put32(trun, 110); put32(trun, -50);
    std::vector<uint8_t> tfhd; put32(tfhd, 1); // track id, no defaults
    std::vector<uint8_t> traf;
    put_bytes(traf, fullbox("tfhd", 0, 0, tfhd));
    put_bytes(traf, fullbox("tfdt", 0, 0, tfdt));
    put_bytes(traf, fullbox("trun", 1, 0x000100 | 0x000200 | 0x000800, trun));
    std::vector<uint8_t> moof;
    put_bytes(moof, mfhd(1));
    put_bytes(moof, box("traf", traf));
    std::vector<uint8_t> file = box("moof", moof);
    write_file(d / "f1.m4s", file);
    auto fi = fmp4::parse_fragment_file((d / "f1.m4s").string(), init);
    FMP4_CHECK(fi.mfhd_sequence == 1);
    FMP4_CHECK(fi.tracks.size() == 1);
    FMP4_CHECK(fi.tracks[0].track_id == 1);
    FMP4_CHECK(fi.tracks[0].decode_start == 9000);
    FMP4_CHECK(fi.tracks[0].sample_count == 2);
    FMP4_CHECK(fi.tracks[0].duration == 6000);
    FMP4_CHECK(fi.mfhd_offset == 8);
  }
  // Fragment 2: tfdt v1 (64-bit), trun v0 without durations -> tfhd default.
  {
    std::vector<uint8_t> tfdt; put64(tfdt, 15000ull);
    std::vector<uint8_t> trun; put32(trun, 3); // count only
    std::vector<uint8_t> tfhd;
    put32(tfhd, 2);          // track id
    put32(tfhd, 1024);       // default sample duration (flag 0x8)
    std::vector<uint8_t> traf;
    put_bytes(traf, fullbox("tfhd", 0, 0x000008, tfhd));
    put_bytes(traf, fullbox("tfdt", 1, 0, tfdt));
    put_bytes(traf, fullbox("trun", 0, 0, trun));
    std::vector<uint8_t> moof;
    put_bytes(moof, mfhd(7));
    put_bytes(moof, box("traf", traf));
    write_file(d / "f2.m4s", box("moof", moof));
    auto fi = fmp4::parse_fragment_file((d / "f2.m4s").string(), init);
    FMP4_CHECK(fi.tracks[0].decode_start == 15000);
    FMP4_CHECK(fi.tracks[0].sample_count == 3);
    FMP4_CHECK(fi.tracks[0].duration == 3072); // tfhd default wins over trex
  }
  // Fragment 3: no trun durations, no tfhd default -> trex fallback.
  {
    std::vector<uint8_t> trun; put32(trun, 2);
    std::vector<uint8_t> tfhd; put32(tfhd, 1);
    std::vector<uint8_t> traf;
    put_bytes(traf, fullbox("tfhd", 0, 0, tfhd));
    put_bytes(traf, fullbox("trun", 0, 0, trun));
    std::vector<uint8_t> moof;
    put_bytes(moof, mfhd(2));
    put_bytes(moof, box("traf", traf));
    write_file(d / "f3.m4s", box("moof", moof));
    auto fi = fmp4::parse_fragment_file((d / "f3.m4s").string(), init);
    FMP4_CHECK(fi.tracks[0].duration == 6000); // 2 * trex 3000
  }
  fs::remove_all(d);
}

FMP4_TEST(parse_fragment_rejects_unknown_track_and_truncation) {
  fs::path d = fs::temp_directory_path() / "fmp4_frag_bad";
  fs::remove_all(d); fs::create_directories(d);
  auto init = two_track_init();
  std::vector<uint8_t> tfhd; put32(tfhd, 99); put32(tfhd, 0);
  std::vector<uint8_t> traf;
  put_bytes(traf, fullbox("tfhd", 0, 0, tfhd));
  std::vector<uint8_t> moof;
  put_bytes(moof, mfhd(1));
  put_bytes(moof, box("traf", traf));
  write_file(d / "bad.m4s", box("moof", moof));
  bool threw = false;
  try { fmp4::parse_fragment_file((d / "bad.m4s").string(), init); }
  catch (const fmp4::Fmp4Error &e) {
    threw = true;
    FMP4_CHECK(e.offset() != 0);
  }
  FMP4_CHECK(threw);

  std::vector<uint8_t> cut;
  put32(cut, 8); putcc(cut, "moof"); // claims 8 but traf payload missing
  write_file(d / "cut.m4s", cut);
  threw = false;
  try { fmp4::parse_fragment_file((d / "cut.m4s").string(), init); }
  catch (const fmp4::Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
  fs::remove_all(d);
}

FMP4_TEST(build_index_detects_missing_and_duplicate) {
  fs::path d = fs::temp_directory_path() / "fmp4_idx_test";
  fs::remove_all(d); fs::create_directories(d);
  // Write a minimal init.mp4 with one trex-backed track (reuse parser shape).
  auto mk_init = [&] {
    std::vector<uint8_t> mdhd_p(20, 0);
    mdhd_p[12] = 0; mdhd_p[13] = 1; mdhd_p[14] = 0x5f; mdhd_p[15] = 0x90;
    auto mdhd = box("mdhd", mdhd_p);
    std::vector<uint8_t> hdlr_p(12, 0);
    hdlr_p[8] = 'v'; hdlr_p[9] = 'i'; hdlr_p[10] = 'd'; hdlr_p[11] = 'e';
    auto hdlr = box("hdlr", hdlr_p);
    std::vector<uint8_t> stsd_p;
    put32(stsd_p, 0); put32(stsd_p, 1);
    put_bytes(stsd_p, box("avc1", std::vector<uint8_t>(8, 0)));
    auto stsd = box("stsd", stsd_p);
    auto stbl = box("stbl", stsd);
    auto minf = box("minf", stbl);
    std::vector<uint8_t> mdia_b; put_bytes(mdia_b, mdhd);
    put_bytes(mdia_b, hdlr); put_bytes(mdia_b, minf);
    std::vector<uint8_t> tkhd_p(20, 0);
    tkhd_p[15] = 1;
    auto tkhd = box("tkhd", tkhd_p);
    std::vector<uint8_t> trak_b; put_bytes(trak_b, tkhd);
    put_bytes(trak_b, box("mdia", mdia_b));
    std::vector<uint8_t> trex_p;
    put32(trex_p, 0);    // version/flags
    put32(trex_p, 1);    // track id
    put32(trex_p, 1);    // sample desc index
    put32(trex_p, 3000); // default sample duration
    trex_p.resize(trex_p.size() + 8); // size + flags
    auto trex = box("trex", trex_p);
    std::vector<uint8_t> moov_b;
    put_bytes(moov_b, box("trak", trak_b));
    put_bytes(moov_b, box("mvex", trex));
    return box("moov", moov_b);
  };
  write_file(d / "init.mp4", mk_init());

  auto mk_frag = [&](uint32_t mfhd_seq, uint32_t track,
                     const std::string &name) {
    std::vector<uint8_t> tfhd; put32(tfhd, track);
    std::vector<uint8_t> trun; put32(trun, 1);
    put32(trun, 3000); // sample duration (trun flag 0x100)
    std::vector<uint8_t> traf;
    put_bytes(traf, fullbox("tfhd", 0, 0, tfhd));
    put_bytes(traf, fullbox("trun", 0, 0x000100, trun));
    std::vector<uint8_t> moof_b;
    put_bytes(moof_b, mfhd(mfhd_seq));
    put_bytes(moof_b, box("traf", traf));
    write_file(d / name, box("moof", moof_b));
  };
  mk_frag(1, 1, "f1.m4s");
  mk_frag(2, 1, "f2.m4s");
  // f3 intentionally omitted; sequence 4 duplicated in the manifest.
  std::ofstream m(d / "fragments.manifest");
  m << R"({"init_segment":"init.mp4","fragments":[
       {"sequence":1,"path":"f1.m4s"},
       {"sequence":2,"path":"f2.m4s"},
       {"sequence":3,"path":"f3.m4s"},
       {"sequence":4,"path":"f4a.m4s"},
       {"sequence":4,"path":"f4b.m4s"}]})";
  m.close();
  mk_frag(4, 1, "f4a.m4s");
  mk_frag(4, 1, "f4b.m4s");

  auto idx = fmp4::build_index(d.string());
  FMP4_CHECK(idx.fragments.size() == 5);
  FMP4_CHECK(idx.missing_sequences.size() == 1);
  FMP4_CHECK(idx.missing_sequences[0] == 3);
  FMP4_CHECK(idx.duplicate_sequences.size() == 1);
  FMP4_CHECK(idx.duplicate_sequences[0] == 4);
  FMP4_CHECK(idx.fragments[0].present && idx.fragments[0].mfhd_sequence == 1);
  FMP4_CHECK(!idx.fragments[2].present);
  FMP4_CHECK(idx.init.tracks[0].timescale == 90000);
  FMP4_CHECK(idx.find_manifest(2) != nullptr);
  fs::remove_all(d);
}

FMP4_TEST(validator_flags_missing_gaps_and_timeline_deltas) {
  using namespace fmp4;
  FragmentIndex idx;
  TrackInit t1; t1.track_id = 1; t1.timescale = 90000;
  t1.sample_entry = FOURCC("avc1"); t1.handler = FOURCC("vide");
  idx.init.tracks = {t1};
  auto add = [&](uint32_t mseq, uint32_t mfhd, bool present,
                 uint64_t tfdt, uint64_t dur) {
    FragmentInfo f;
    f.manifest_seq = mseq; f.rel_path = "f" + std::to_string(mseq) + ".m4s";
    f.present = present; f.mfhd_sequence = mfhd; f.mfhd_offset = 8;
    if (present) {
      TrackRun r; r.track_id = 1; r.decode_start = tfdt; r.duration = dur;
      f.tracks = {r};
    }
    idx.fragments.push_back(f);
  };
  add(1, 1, true, 0, 3000);
  add(2, 2, true, 3000, 3000);
  add(3, 3, false, 0, 0);      // missing file
  add(4, 5, true, 9000, 3000); // mfhd gap: 4 skipped
  for (uint32_t s : {3}) idx.missing_sequences.push_back(s);

  auto rep = validate_index(idx);
  FMP4_CHECK(!rep.ok());
  FMP4_CHECK(rep.mfhd_contiguous == false);
  FMP4_CHECK(rep.mfhd_gaps.size() == 2);
  FMP4_CHECK(rep.mfhd_gaps[0] == 3 && rep.mfhd_gaps[1] == 4);
  bool has_missing = false, has_gap = false;
  for (const auto &i : rep.issues) {
    if (i.kind == IssueKind::MissingFragment && i.sequence == 3)
      has_missing = true;
    if (i.kind == IssueKind::MfhdGap) has_gap = true;
  }
  FMP4_CHECK(has_missing && has_gap);
  const auto *tl = rep.timeline(1);
  FMP4_CHECK(tl && tl->decode_start == 0 && tl->total_duration == 9000);
  FMP4_CHECK(ticks_to_seconds(9000, 90000) == 0.1);

  // Baseline comparison: identical timeline => zero deltas.
  FragmentIndex copy = idx;
  // Replace the missing fragment with a present one in the "output".
  FragmentInfo &m = copy.fragments[2];
  m.present = true; m.mfhd_sequence = 4;
  TrackRun r; r.track_id = 1; r.decode_start = 6000; r.duration = 3000;
  m.tracks = {r};
  copy.missing_sequences.clear();
  auto out_rep = validate_against_baseline(copy, idx);
  const auto *otl = out_rep.timeline(1);
  FMP4_CHECK(otl && otl->decode_start_delta == 0);
  FMP4_CHECK(otl->duration_delta == 3000); // output gained one fragment
}

FMP4_TEST(validator_detects_sample_entry_drift) {
  using namespace fmp4;
  FragmentIndex base, out;
  TrackInit b; b.track_id = 1; b.timescale = 90000;
  b.sample_entry = FOURCC("avc1"); b.handler = FOURCC("vide");
  TrackInit o = b; o.sample_entry = FOURCC("encv");
  base.init.tracks = {b};
  out.init.tracks = {o};
  auto rep = validate_against_baseline(out, base);
  bool drift = false;
  for (const auto &i : rep.issues)
    if (i.kind == IssueKind::IncompatibleSampleEntry) drift = true;
  FMP4_CHECK(drift);
}

FMP4_TEST(repack_rewrites_mfhd_and_preserves_bytes) {
  using namespace fmp4;
  fs::path base = fs::temp_directory_path() / "fmp4_repack_in";
  fs::path out = fs::temp_directory_path() / "fmp4_repack_out";
  fs::remove_all(base); fs::remove_all(out);
  fs::create_directories(base);

  // One-track init segment.
  {
    std::vector<uint8_t> mdhd_p(20, 0);
    mdhd_p[13] = 1; mdhd_p[14] = 0x5f; mdhd_p[15] = 0x90; // 90000
    std::vector<uint8_t> hdlr_p(12, 0);
    hdlr_p[8] = 'v'; hdlr_p[9] = 'i'; hdlr_p[10] = 'd'; hdlr_p[11] = 'e';
    std::vector<uint8_t> stsd_p; put32(stsd_p, 0); put32(stsd_p, 1);
    put_bytes(stsd_p, box("avc1", std::vector<uint8_t>(8, 0)));
    std::vector<uint8_t> mdia_b;
    put_bytes(mdia_b, box("mdhd", mdhd_p));
    put_bytes(mdia_b, box("hdlr", hdlr_p));
    put_bytes(mdia_b, box("minf", box("stbl", box("stsd", stsd_p))));
    std::vector<uint8_t> tkhd_p(20, 0); tkhd_p[15] = 1;
    std::vector<uint8_t> trak_b;
    put_bytes(trak_b, box("tkhd", tkhd_p));
    put_bytes(trak_b, box("mdia", mdia_b));
    std::vector<uint8_t> trex_p;
    put32(trex_p, 0); put32(trex_p, 1); put32(trex_p, 1);
    put32(trex_p, 3000); trex_p.resize(trex_p.size() + 8);
    std::vector<uint8_t> moov_b;
    put_bytes(moov_b, box("trak", trak_b));
    put_bytes(moov_b, box("mvex", box("trex", trex_p)));
    write_file(base / "init.mp4", box("moov", moov_b));
  }
  auto mkfrag = [&](const std::string &name, uint32_t mseq,
                    const std::vector<uint8_t> &marker) {
    std::vector<uint8_t> tfhd; put32(tfhd, 1);
    std::vector<uint8_t> trun; put32(trun, 1); put32(trun, 3000);
    std::vector<uint8_t> traf;
    put_bytes(traf, fullbox("tfhd", 0, 0, tfhd));
    put_bytes(traf, fullbox("trun", 0, 0x000100, trun));
    std::vector<uint8_t> moof_b;
    put_bytes(moof_b, mfhd(mseq));
    put_bytes(moof_b, box("traf", traf));
    std::vector<uint8_t> file = box("moof", moof_b);
    put_bytes(file, box("free", marker)); // unknown/skipped box
    put_bytes(file, box("mdat", marker)); // media bytes preserved
    write_file(base / name, file);
  };
  std::vector<uint8_t> m1 = {0xa1, 0xb2, 0xc3};
  std::vector<uint8_t> m2 = {0x0d, 0x0e, 0x0f};
  mkfrag("frag-1.m4s", 1, m1);
  mkfrag("frag-2.m4s", 3, m2); // mfhd gap (1 then 3)
  std::ofstream m(base / "fragments.manifest");
  m << R"({"init_segment":"init.mp4","fragments":[
       {"sequence":1,"path":"frag-1.m4s"},
       {"sequence":2,"path":"frag-2.m4s"}]})";
  m.close();

  RepackResult r = repack_directory(base.string(), out.string());
  FMP4_CHECK(r.input_fragment_count == 2);
  FMP4_CHECK(r.output_fragment_count == 2);
  FMP4_CHECK(r.mfhd_before.size() == 2 && r.mfhd_before[0] == 1 &&
             r.mfhd_before[1] == 3);
  FMP4_CHECK(r.mfhd_after.size() == 2 && r.mfhd_after[0] == 1 &&
             r.mfhd_after[1] == 2);
  FMP4_CHECK(r.verification.ok());

  auto out_idx = build_index(out.string());
  FMP4_CHECK(out_idx.fragments.size() == 2);
  FMP4_CHECK(out_idx.fragments[0].mfhd_sequence == 1);
  FMP4_CHECK(out_idx.fragments[1].mfhd_sequence == 2);
  auto out_rep = validate_index(out_idx);
  FMP4_CHECK(out_rep.mfhd_contiguous);

  auto read_all = [](const fs::path &p) {
    std::ifstream is(p, std::ios::binary);
    return std::vector<uint8_t>((std::istreambuf_iterator<char>(is)),
                                std::istreambuf_iterator<char>());
  };
  std::pair<std::string, std::vector<uint8_t>> cases[] = {
      {"frag-1.m4s", m1}, {"frag-2.m4s", m2}};
  for (auto &pair : cases) {
    auto bytes = read_all(out / pair.first);
    FMP4_CHECK(std::search(bytes.begin(), bytes.end(), pair.second.begin(),
                           pair.second.end()) != bytes.end());
  }
  // Output files stay the same length (only 4 patched bytes).
  FMP4_CHECK(fs::file_size(out / "frag-1.m4s") ==
             fs::file_size(base / "frag-1.m4s"));

  // Missing fragment: structural failure, no partial output remains.
  fs::path base2 = fs::temp_directory_path() / "fmp4_repack_missing";
  fs::path out2 = fs::temp_directory_path() / "fmp4_repack_missing_out";
  fs::remove_all(base2); fs::remove_all(out2);
  fs::create_directories(base2);
  fs::copy_file(base / "init.mp4", base2 / "init.mp4");
  fs::copy_file(base / "frag-1.m4s", base2 / "frag-1.m4s");
  std::ofstream m2f(base2 / "fragments.manifest");
  m2f << R"({"init_segment":"init.mp4","fragments":[
       {"sequence":1,"path":"frag-1.m4s"},
       {"sequence":2,"path":"frag-2.m4s"}]})";
  m2f.close();
  bool threw = false;
  try { repack_directory(base2.string(), out2.string()); }
  catch (const RepackError &e) {
    threw = true;
    FMP4_CHECK(e.exit_code() == 3);
    FMP4_CHECK(e.missing_sequences.size() == 1 &&
               e.missing_sequences[0] == 2);
  }
  FMP4_CHECK(threw);
  FMP4_CHECK(!fs::exists(out2));
  fs::remove_all(base); fs::remove_all(out);
  fs::remove_all(base2); fs::remove_all(out2);
}

FMP4_TEST(fixture_writer_two_tracks_mixed_versions_and_gap) {
  using namespace fmp4;
  fs::path d = fs::temp_directory_path() / "fmp4_fixture_ok";
  fs::remove_all(d);
  fixture::Options opt;
  opt.dir = d.string();
  opt.segments = 6;
  opt.mixed_versions = true;
  opt.mfhd_gap_at = 4;
  auto seqs = fixture::write_fixture(opt);
  FMP4_CHECK(seqs.size() == 6);
  // 101,102,103, then a skip at segment 4, then 105,106.
  FMP4_CHECK(seqs[0] == 101 && seqs[2] == 103 && seqs[3] == 105);
  FMP4_CHECK(seqs[5] == 107);

  auto idx = build_index(d.string());
  FMP4_CHECK(idx.fragments.size() == 6);
  FMP4_CHECK(idx.missing_sequences.empty());
  FMP4_CHECK(idx.init.tracks.size() == 2);
  FMP4_CHECK(idx.init.tracks[0].timescale == 90000);
  FMP4_CHECK(idx.init.tracks[1].timescale == 48000);
  auto rep = validate_index(idx);
  FMP4_CHECK(!rep.mfhd_contiguous);
  FMP4_CHECK(rep.mfhd_gaps.size() == 1 && rep.mfhd_gaps[0] == 104);
  const auto *v = rep.timeline(1);
  const auto *a = rep.timeline(2);
  FMP4_CHECK(v && a);
  FMP4_CHECK(v->decode_start == 0);
  FMP4_CHECK(v->total_duration == 6 * 3000);
  FMP4_CHECK(a->total_duration == 6 * 1024);
  FMP4_CHECK(ticks_to_seconds(v->total_duration, 90000) == 0.2);
  fs::remove_all(d);
}

FMP4_TEST(fixture_writer_omit_corrupt_and_incompatible) {
  using namespace fmp4;
  fs::path d = fs::temp_directory_path() / "fmp4_fixture_bad";
  fs::remove_all(d);
  fixture::Options opt;
  opt.dir = d.string();
  opt.segments = 6;
  opt.omit_file_for_seq = 4;
  fixture::write_fixture(opt);
  auto idx = build_index(d.string());
  FMP4_CHECK(idx.missing_sequences.size() == 1);
  FMP4_CHECK(idx.missing_sequences[0] == 4);
  FMP4_CHECK(!idx.fragments[3].present);
  fs::remove_all(d);

  fs::path d2 = fs::temp_directory_path() / "fmp4_fixture_corrupt";
  fs::remove_all(d2);
  fixture::Options c;
  c.dir = d2.string();
  c.segments = 3;
  c.corrupt_length_at = 2;
  fixture::write_fixture(c);
  bool threw = false;
  try { build_index(d2.string()); }
  catch (const Fmp4Error &) { threw = true; }
  FMP4_CHECK(threw);
  fs::remove_all(d2);

  fs::path d3 = fs::temp_directory_path() / "fmp4_fixture_incompat";
  fs::remove_all(d3);
  fixture::Options g;
  g.dir = d3.string();
  g.segments = 2;
  g.incompatible_init = true;
  fixture::write_fixture(g);
  auto bad_init = parse_init_segment((d3 / "init.mp4").string());
  FMP4_CHECK(bad_init.tracks[0].sample_entry == FOURCC("encv"));
  fs::remove_all(d3);
}
