// Test harness; parser/index/validator tests are added in later batches.
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <filesystem>
#include <functional>
#include <vector>

#include "fmp4/box_reader.hpp"
#include "fmp4/safe_path.hpp"

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
