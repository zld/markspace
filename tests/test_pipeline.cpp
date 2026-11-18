#include "fixture_builder.hpp"
#include "fragment_index.hpp"
#include "compatibility.hpp"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <cstring>

using namespace mp4;
using namespace mp4::fix;

namespace {

MediaPackage make_package(
    const std::vector<std::pair<std::string, std::vector<uint8_t>>>& files,
    const Limits& lim = {}) {
    MediaPackage pkg;
    auto init = std::make_unique<MediaFile>();
    init->data = build_init();
    init->boxes = parse_boxes(init->data);
    init->is_init = true;
    pkg.index = parse_init_segment(init->data, lim);
    pkg.init = std::move(init);
    for (const auto& [name, bytes] : files) {
        auto f = std::make_unique<MediaFile>();
        f->path = name;
        f->data = bytes;
        f->boxes = parse_boxes(bytes, {lim.max_file_size});
        index_media_segment(pkg.index, *f, lim);
        pkg.segments.push_back(std::move(f));
    }
    return pkg;
}

}  // namespace

TEST_CASE("box parser accepts normal, largesize and size=0 boxes") {
    auto set = build_normal_set();
    REQUIRE_NOTHROW(parse_boxes(build_init()));
    REQUIRE_NOTHROW(parse_boxes(set[0].second));          // plain
    REQUIRE_NOTHROW(parse_boxes(set[4].second));          // largesize moof
    REQUIRE_NOTHROW(parse_boxes(set[5].second));          // size=0 mdat
    auto boxes = parse_boxes(set[5].second);
    const Box* mdat = find_box(boxes, "mdat");
    REQUIRE(mdat != nullptr);
    REQUIRE(mdat->size == set[5].second.size() - mdat->offset);
}

TEST_CASE("box parser rejects truncated header") {
    auto set = build_normal_set();
    auto cut = set[0].second;
    cut.resize(cut.size() / 2);
    REQUIRE_THROWS_AS(parse_boxes(cut), BoxError);
}

TEST_CASE("box parser rejects out-of-range length") {
    auto bytes = build_init();
    bytes[0] = 0x7f;  // claim an enormous top-level box
    REQUIRE_THROWS_AS(parse_boxes(bytes), BoxError);
}

TEST_CASE("box parser rejects size smaller than eight") {
    std::vector<uint8_t> b = {0, 0, 0, 3, 'f', 'r', 'e', 'e'};
    REQUIRE_THROWS_AS(parse_boxes(b), BoxError);
}

TEST_CASE("box parser rejects unknown critical nested box") {
    auto set = build_normal_set();
    auto bytes = set[0].second;
    // Inject an unknown box as the first child of the moof.
    size_t moof = bytes.size();
    for (size_t i = 0; i + 4 <= bytes.size(); ++i)
        if (std::memcmp(bytes.data() + i, "moof", 4) == 0) {
            moof = i - 4;
            break;
        }
    std::vector<uint8_t> injected = {0, 0, 0, 8, 'z', 'z', 'z', 'z'};
    bytes.insert(bytes.begin() + long(moof + 8), injected.begin(),
                 injected.end());
    // Fix the moof size field to include the injected box.
    uint32_t sz;
    std::memcpy(&sz, bytes.data() + moof, 4);
    sz = __builtin_bswap32(__builtin_bswap32(sz) + 8);
    std::memcpy(bytes.data() + moof, &sz, 4);
    REQUIRE_THROWS_AS(parse_boxes(bytes), BoxError);
}

TEST_CASE("box parser rejects encrypted boxes") {
    auto set = build_fault_set(Fault::Encrypted);
    REQUIRE_THROWS_AS(parse_boxes(set[0].second), BoxError);
}

TEST_CASE("init segment yields two configured tracks") {
    MediaIndex idx = parse_init_segment(build_init());
    REQUIRE(idx.tracks.size() == 2);
    REQUIRE(idx.tracks.at(1).timescale == 90000);
    REQUIRE(idx.tracks.at(2).timescale == 48000);
    REQUIRE(idx.tracks.at(1).default_sample_duration == 1000);
    REQUIRE(idx.tracks.at(2).default_sample_size == 12);
    REQUIRE_FALSE(idx.tracks.at(1).codec_config_sha256.empty());
}

TEST_CASE("index builds fragments, tracks and sequences") {
    auto pkg = make_package(build_normal_set());
    REQUIRE(pkg.index.fragments.size() == 6);
    for (uint32_t i = 0; i < 6; ++i)
        REQUIRE(pkg.index.fragments[i].sequence_number == i + 1);
    const Fragment& f0 = pkg.index.fragments[0];
    REQUIRE(f0.tracks.size() == 2);
    REQUIRE(f0.tracks[0].track_id == 1);
    REQUIRE(f0.tracks[0].samples.size() == 2);
    REQUIRE(f0.tracks[1].samples.size() == 3);
    // Negative composition offset on fragment 3 (trun v1).
    REQUIRE(pkg.index.fragments[2].tracks[0].samples[0].composition_offset ==
            -3600);
}

TEST_CASE("index detects sample data length mismatch") {
    auto set = build_fault_set(Fault::Length);
    REQUIRE_THROWS_AS(make_package(set), IndexError);
}

TEST_CASE("index rejects fragment referencing unknown track") {
    auto set = build_normal_set();
    auto& bytes = set[0].second;
    for (size_t i = 0; i + 4 <= bytes.size(); ++i)
        if (std::memcmp(bytes.data() + i, "tfhd", 4) == 0) {
            size_t id = i + 8;  // type(4)+version/flags(4)
            bytes[id] = 0; bytes[id + 1] = 0; bytes[id + 2] = 0;
            bytes[id + 3] = 9;
            break;
        }
    REQUIRE_THROWS_AS(make_package(set), IndexError);
}

TEST_CASE("compatibility passes the normal set with zero deviation") {
    auto pkg = make_package(build_normal_set());
    auto rep = check_compatibility(pkg.index);
    REQUIRE(rep.ok);
    REQUIRE(rep.errors.empty());
    REQUIRE(rep.missing_fragments.empty());
    for (const auto& d : rep.track_deviations)
        REQUIRE(d.max_abs_deviation_ticks == 0);
}

TEST_CASE("compatibility flags duplicate sequence as a hard error") {
    auto pkg = make_package(build_fault_set(Fault::Duplicate));
    auto rep = check_compatibility(pkg.index);
    REQUIRE_FALSE(rep.ok);
    bool dup = false;
    for (const auto& s : rep.sequence_issues)
        dup |= s.category == SequenceIssue::Duplicate;
    REQUIRE(dup);
}

TEST_CASE("compatibility treats out-of-order as a recoverable warning") {
    auto pkg = make_package(build_fault_set(Fault::OutOfOrder));
    auto rep = check_compatibility(pkg.index);
    REQUIRE(rep.ok);
    bool ooo = false;
    for (const auto& s : rep.sequence_issues)
        ooo |= s.category == SequenceIssue::OutOfOrder;
    REQUIRE(ooo);
}

TEST_CASE("compatibility locates the missing sequence and its neighbours") {
    auto pkg = make_package(build_fault_set(Fault::Missing));
    auto rep = check_compatibility(pkg.index);
    REQUIRE(rep.ok);  // gaps are reported, not fatal
    REQUIRE(rep.missing_fragments.size() == 1);
    REQUIRE(rep.missing_fragments[0].expected_sequence == 4);
    REQUIRE(rep.missing_fragments[0].previous_actual == 3);
    REQUIRE(rep.missing_fragments[0].next_actual == 5);
}

TEST_CASE("compatibility reports parameter drift and blocks writing") {
    auto pkg = make_package(build_fault_set(Fault::Drift));
    auto rep = check_compatibility(pkg.index);
    REQUIRE_FALSE(rep.ok);
    bool drift = false;
    for (const auto& c : rep.config_issues)
        drift |= c.category == ConfigIssue::DefaultDurationDrift;
    REQUIRE(drift);
}

TEST_CASE("missing track in every fragment is reported") {
    auto set = build_normal_set();
    auto pkg = make_package(set);
    // Add a phantom track to the init config only.
    TrackConfig extra;
    extra.track_id = 7;
    extra.timescale = 1000;
    pkg.index.tracks[7] = extra;
    auto rep = check_compatibility(pkg.index);
    bool missing = false;
    for (const auto& c : rep.config_issues)
        missing |= c.category == ConfigIssue::MissingTrack &&
                   c.track_id == 7;
    REQUIRE(missing);
}
