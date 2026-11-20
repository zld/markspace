#include "h264/self_test.hpp"

#include <cstdio>
#include <functional>
#include <string>
#include <vector>

#include "h264/bit_reader.hpp"
#include "h264/syntax.hpp"
#include "h264/bit_writer.hpp"
#include "h264/nal.hpp"

namespace h264 {

namespace {

int g_failures = 0;
int g_checks = 0;
bool g_verbose = false;

void check(bool condition, const std::string& name) {
  ++g_checks;
  if (g_verbose && condition) std::printf("ok: %s\n", name.c_str());
  if (!condition) {
    ++g_failures;
    std::printf("FAIL: %s\n", name.c_str());
  }
}

void expect_throw(const std::string& name, const std::function<void()>& body) {
  bool threw = false;
  try {
    body();
  } catch (const ParseError&) {
    threw = true;
  } catch (...) {
    check(false, name + " (unexpected exception type)");
    return;
  }
  check(threw, name);
}

void expect_no_throw(const std::string& name,
                     const std::function<void()>& body) {
  try {
    body();
    check(true, name);
  } catch (const ParseError& error) {
    check(false, name + " (" + error.diagnostic().message + ")");
  }
}

std::vector<std::uint8_t> make_sps_rbsp(int profile_idc, int level_idc,
                                        int width_mbs, int height_map_units,
                                        bool frame_mbs_only) {
  BitWriter w;
  w.write_bits(static_cast<std::uint32_t>(profile_idc), 8);
  w.write_bits(0, 8);
  w.write_bits(static_cast<std::uint32_t>(level_idc), 8);
  w.write_ue(0);  // sps_id
  w.write_ue(0);  // log2_max_frame_num_minus4
  w.write_ue(0);  // pic_order_cnt_type
  w.write_ue(2);  // log2_max_pic_order_cnt_lsb_minus4
  w.write_ue(1);  // max_num_ref_frames
  w.write_bit(false);
  w.write_ue(static_cast<std::uint32_t>(width_mbs - 1));
  w.write_ue(static_cast<std::uint32_t>(height_map_units - 1));
  w.write_bit(frame_mbs_only);
  if (!frame_mbs_only) w.write_bit(false);
  w.write_bit(true);
  w.write_bit(false);
  w.write_bit(false);
  w.append_trailing_bits();
  return w.bytes();
}

// --- 1. Exp-Golomb edges -------------------------------------------------

void test_exp_golomb() {
  const std::uint8_t bits[] = {0b10100110, 0b01000000};
  BitReader r(bits, sizeof(bits), 0);
  check(r.read_ue() == 0, "ue 0");
  check(r.read_ue() == 1, "ue 1");
  check(r.read_ue() == 2, "ue 2");
  check(r.read_ue() == 3, "ue 3");

  BitWriter w;
  w.write_ue(0);
  w.write_ue(1);
  w.write_ue(0xFFFFFFFFu - 1);
  BitReader rt(w.bytes().data(), w.bytes().size(), 0);
  check(rt.read_ue() == 0, "ue round-trip 0");
  check(rt.read_ue() == 1, "ue round-trip 1");
  check(rt.read_ue() == 0xFFFFFFFEu, "ue round-trip 32-bit max");

  // se mapping: 0 -> 0, 1 -> 1, -1 -> 2, 2 -> 3, -2 -> 4
  const std::uint8_t se_bits[] = {0b10100110, 0b01000000};
  BitReader sr(se_bits, sizeof(se_bits), 0);
  check(sr.read_se() == 0, "se 0");
  check(sr.read_se() == 1, "se 1");
  check(sr.read_se() == -1, "se -1");
  check(sr.read_se() == 2, "se 2");

  // 32 leading zeros -> prefix too long
  const std::uint8_t long_prefix[] = {0, 0, 0, 0, 0b01000000};
  expect_throw("ue prefix too long", [&] {
    BitReader lr(long_prefix, sizeof(long_prefix), 7);
    lr.read_ue();
  });
  // Truncated code: all zeros to end of buffer.
  const std::uint8_t truncated[] = {0x00, 0x00};
  expect_throw("ue truncation", [&] {
    BitReader tr(truncated, sizeof(truncated), 0);
    tr.read_ue();
  });
  expect_throw("fixed bits overrun", [&] {
    BitReader fr(bits, sizeof(bits), 0);
    fr.skip_bits(15);
    fr.read_bits(2);
  });

  // more_rbsp_data: the stop bit is the last set bit, not the first one in
  // the final byte.
  const std::uint8_t rbsp_1[] = {0xC0};  // 1100 0000, stop bit at index 1
  BitReader mr1(rbsp_1, sizeof(rbsp_1), 0);
  check(mr1.more_rbsp_data(), "more_rbsp_data before data");
  mr1.skip_bits(1);
  check(!mr1.more_rbsp_data(), "more_rbsp_data at stop bit");
  const std::uint8_t rbsp_2[] = {0x80, 0x00};  // stop bit in first byte
  BitReader mr2(rbsp_2, sizeof(rbsp_2), 0);
  check(!mr2.more_rbsp_data(), "more_rbsp_data with zero tail");
  const std::uint8_t rbsp_3[] = {0x00, 0x80};  // stop bit in last byte
  BitReader mr3(rbsp_3, sizeof(rbsp_3), 0);
  check(mr3.more_rbsp_data(), "more_rbsp_data across zero byte");
  check(rbsp_unescape(rbsp_3, sizeof(rbsp_3), 0).size() == 2,
        "rbsp unescape keeps non-escaped zeros");
}

// --- 2. Escape bytes and RBSP handling ------------------------------------

void test_rbsp_escape() {
  const std::uint8_t escaped[] = {0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x01};
  const std::vector<std::uint8_t> rbsp =
      rbsp_unescape(escaped, sizeof(escaped), 0);
  const std::vector<std::uint8_t> expected = {0, 0, 0, 0, 1};
  check(rbsp == expected, "rbsp unescape 00 00 03");

  const std::vector<std::uint8_t> source = {0x00, 0x00, 0x01, 0x2A, 0x00, 0x00};
  const std::vector<std::uint8_t> reescaped = rbsp_escape(source);
  check(rbsp_unescape(reescaped.data(), reescaped.size(), 0) == source,
        "rbsp escape round-trip");

  const std::uint8_t bad[] = {0x00, 0x00, 0x03, 0xFF};
  expect_throw("invalid emulation prevention", [&] {
    rbsp_unescape(bad, sizeof(bad), 0);
  });
}

// --- 3. Start codes -------------------------------------------------------

void test_start_codes() {
  const std::vector<std::uint8_t> data = {
      0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1E,   // SPS (3-byte)
      0x00, 0x00, 0x00, 0x01, 0x68, 0xCE, 0x38, 0x80,  // PPS (4-byte)
      0x00, 0x00, 0x01, 0x00, 0x00, 0x01, 0x65, 0x88};  // empty NAL, then IDR
  const AnnexBScan scan = scan_annexb(data, 64u * 1024u * 1024u);
  expect_no_throw("valid start codes do not throw",
                  [&] { scan_annexb(data, 64u * 1024u * 1024u); });
  check(scan.nals.size() == 4, "start-code scan NAL count");
  check(scan.nals[0].offset == 0, "3-byte start code offset");
  check(scan.nals[0].nal_unit_type == 7, "3-byte SPS type");
  check(scan.nals[1].offset == 7, "4-byte start code offset");
  check(scan.nals[1].nal_unit_type == 8, "4-byte PPS type");
  check(scan.empty_nal_count == 1, "empty NAL detected");
  check(scan.nals[2].payload_size == 0, "empty NAL payload");
  check(scan.nals[3].offset == 18, "IDR offset after empty NAL");
  check(scan.nals[3].nal_unit_type == 5, "IDR after empty NAL");

  const std::vector<std::uint8_t> leading = {0, 0, 0, 0x00, 0x00, 0x01, 0x67};
  const AnnexBScan with_leading =
      scan_annexb(leading, 64u * 1024u * 1024u);
  // The zero_byte at index 2 belongs to the four-byte start code, so only the
  // first two zeros are leading_zero_8bits and the offset stays exact.
  check(with_leading.leading_zero_bytes == 2, "leading zero count");
  check(!with_leading.nals.empty() && with_leading.nals[0].offset == 2,
        "first NAL after leading zeros");

  const std::vector<std::uint8_t> truncated = {0x00, 0x00, 0x01};
  const AnnexBScan truncated_scan =
      scan_annexb(truncated, 64u * 1024u * 1024u);
  check(truncated_scan.had_truncated_start_code,
        "truncated start code detected");
}

// --- 4. SPS / PPS parsing -------------------------------------------------

void test_parameter_sets() {
  const std::vector<std::uint8_t> rbsp = make_sps_rbsp(66, 30, 4, 2, false);
  const Sps sps = parse_sps(rbsp.data(), rbsp.size(), 0);
  check(sps.profile_idc == 66, "SPS profile_idc");
  check(sps.level_idc == 30, "SPS level_idc");
  check(sps.pic_width_in_mbs == 4, "SPS width in MBs");
  check(sps.pic_height_in_map_units == 2, "SPS height in map units");
  check(!sps.frame_mbs_only_flag, "SPS frame_mbs_only_flag");

  expect_throw("truncated SPS", [&] {
    parse_sps(rbsp.data(), 3, 0);
  });
  const std::vector<std::uint8_t> truncated = {0x42, 0x00, 0x1E, 0x80};
  expect_throw("SPS ends mid-syntax", [&] {
    parse_sps(truncated.data(), truncated.size(), 0);
  });
}

}  // namespace

int run_self_tests(bool verbose) {
  g_verbose = verbose;
  g_failures = 0;
  g_checks = 0;
  test_exp_golomb();
  test_rbsp_escape();
  test_start_codes();
  test_parameter_sets();
  if (g_failures == 0) {
    std::printf("self-test: %d checks passed\n", g_checks);
  } else {
    std::printf("self-test: %d of %d checks failed\n", g_failures, g_checks);
  }
  return g_failures == 0 ? 0 : 4;
}

}  // namespace h264
