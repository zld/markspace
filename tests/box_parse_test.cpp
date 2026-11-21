#include "box_reader.hpp"
#include "box_writer.hpp"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

using namespace mp4flow;

namespace {
Bytes wrap(uint32_t type, const Bytes& payload) {
  Bytes out;
  BoxWriter w(out);
  w.beginBox(type);
  w.bytes(payload);
  w.endBox();
  return out;
}
} // namespace

TEST_CASE("32-bit box round trip", "[box]") {
  Bytes p = {1, 2, 3, 4};
  Bytes b = wrap(fourcc("mdat"), p);
  auto boxes = parseBoxes(b.data(), 0, b.size(), true);
  REQUIRE(boxes.size() == 1);
  CHECK(boxes[0].type == fourcc("mdat"));
  CHECK(boxes[0].size == 12);
  CHECK(boxes[0].headerSize == 8);
  CHECK(boxes[0].payloadSize() == 4);
}

TEST_CASE("64-bit largesize box", "[box]") {
  Bytes b;
  // Hand-build: size32=1, type, 64-bit size=24, 8 payload bytes.
  b.insert(b.end(), {0, 0, 0, 1});
  b.insert(b.end(), {'m', 'd', 'a', 't'});
  uint64_t sz = 24;
  for (int sh = 56; sh >= 0; sh -= 8) b.push_back(uint8_t(sz >> sh));
  b.insert(b.end(), 8, 0xab);
  auto boxes = parseBoxes(b.data(), 0, b.size(), true);
  REQUIRE(boxes.size() == 1);
  CHECK(boxes[0].headerSize == 16);
  CHECK(boxes[0].size == 24);
  CHECK(boxes[0].payloadSize() == 8);
}

TEST_CASE("size-zero box runs to enclosing end", "[box]") {
  Bytes b;
  b.insert(b.end(), {0, 0, 0, 0});
  b.insert(b.end(), {'m', 'd', 'a', 't'});
  b.insert(b.end(), 10, 0);
  auto boxes = parseBoxes(b.data(), 0, b.size(), true);
  REQUIRE(boxes.size() == 1);
  CHECK(boxes[0].size == b.size()); // 8-byte header + 10 payload = 18
}

TEST_CASE("recursive container and unknown box retained", "[box]") {
  Bytes b;
  BoxWriter w(b);
  w.beginBox(fourcc("moof"));
  w.beginBox(fourcc("traf"));
  w.beginBox(fourcc("zzzz")); // unknown leaf
  w.u32(0xdeadbeef);
  w.endBox();
  w.endBox();
  w.endBox();
  auto boxes = parseBoxes(b.data(), 0, b.size(), true);
  REQUIRE(boxes.size() == 1);
  const Box* traf = boxes[0].findChild(fourcc("traf"));
  REQUIRE(traf != nullptr);
  const Box* unk = traf->findChild(fourcc("zzzz"));
  REQUIRE(unk != nullptr);
  CHECK(unk->payloadSize() == 4);
}

TEST_CASE("truncated header is rejected", "[box][error]") {
  Bytes b = {0, 0, 0, 20, 'm', 'd'};
  REQUIRE_THROWS_AS(parseBoxes(b.data(), 0, b.size(), true), Mp4Error);
}

TEST_CASE("declared size past range is rejected", "[box][error]") {
  Bytes b;
  b.insert(b.end(), {0, 0, 0, 40});
  b.insert(b.end(), {'m', 'd', 'a', 't'});
  b.insert(b.end(), 4, 0);
  try {
    parseBoxes(b.data(), 0, b.size(), true);
    FAIL("expected Mp4Error");
  } catch (const Mp4Error& e) {
    CHECK(e.kind == ErrorKind::Truncated);
  }
}

TEST_CASE("sub-header size smaller than header is rejected", "[box][error]") {
  Bytes b;
  b.insert(b.end(), {0, 0, 0, 1});
  b.insert(b.end(), {'m', 'd', 'a', 't'});
  b.insert(b.end(), {0, 0, 0, 0, 0, 0, 0, 8}); // largesize 8 < 16
  b.insert(b.end(), 8, 0);
  REQUIRE_THROWS_AS(parseBoxes(b.data(), 0, b.size(), true), Mp4Error);
}

TEST_CASE("cursor enforces bounds", "[box]") {
  Bytes b(16, 0);
  Cursor c(b.data(), b.size(), 0, "test");
  CHECK(c.u64() == 0);
  CHECK(c.remaining() == 8);
  (void)c.u64(); // consume the remaining 8
  CHECK(c.remaining() == 0);
  REQUIRE_THROWS_AS(c.u32(), Mp4Error);
}

TEST_CASE("checkedAdd detects overflow", "[box]") {
  REQUIRE_THROWS_AS(checkedAdd(UINT64_MAX, 1, "x"), Mp4Error);
  CHECK(checkedAdd(UINT64_MAX - 1, 1, "x") == UINT64_MAX);
}
