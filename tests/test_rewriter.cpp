#include "fixture_builder.hpp"
#include "fragment_index.hpp"
#include "compatibility.hpp"
#include "rewriter.hpp"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <unistd.h>

using namespace mp4;
using namespace mp4::fix;
namespace fs = std::filesystem;

namespace {

MediaPackage make_package(
    const std::vector<std::pair<std::string, std::vector<uint8_t>>>& files) {
    MediaPackage pkg;
    auto init = std::make_unique<MediaFile>();
    init->data = build_init();
    init->boxes = parse_boxes(init->data);
    init->is_init = true;
    pkg.index = parse_init_segment(init->data);
    pkg.init = std::move(init);
    for (const auto& [name, bytes] : files) {
        auto f = std::make_unique<MediaFile>();
        f->path = name;
        f->data = bytes;
        f->boxes = parse_boxes(bytes);
        index_media_segment(pkg.index, *f);
        pkg.segments.push_back(std::move(f));
    }
    return pkg;
}

std::string unique_dir() {
    static int counter = 0;
    auto path = fs::temp_directory_path() /
                ("mp4repack_test_" + std::to_string(::getpid()) + "_" +
                 std::to_string(counter++));
    return path.string();
}

// Concatenates every mdat payload across the package, in sequence order.
std::vector<uint8_t> mdat_bytes(
    const std::vector<const Fragment*>& order) {
    std::vector<uint8_t> out;
    for (const Fragment* f : order) {
        const auto& d = f->source->data;
        out.insert(out.end(), d.begin() + f->mdat_payload_offset,
                   d.begin() + f->mdat_payload_offset + f->mdat_payload_size);
    }
    return out;
}

}  // namespace

TEST_CASE("rewriter emits continuous sequence numbers from one") {
    auto pkg = make_package(build_fault_set(Fault::OutOfOrder));
    auto rep = check_compatibility(pkg.index);
    REQUIRE(rep.ok);
    auto rw = rewrite_package(pkg, rep);
    REQUIRE(rw.fragments.size() == 6);
    for (size_t i = 0; i < rw.fragments.size(); ++i)
        REQUIRE(rw.fragments[i].sequence == i + 1);
}

TEST_CASE("rewriter preserves every sample byte and track order") {
    auto pkg = make_package(build_normal_set());
    auto rep = check_compatibility(pkg.index);
    auto rw = rewrite_package(pkg, rep);

    std::vector<const Fragment*> order;
    for (const Fragment& f : pkg.index.fragments) order.push_back(&f);
    std::sort(order.begin(), order.end(),
              [](const Fragment* a, const Fragment* b) {
                  return a->sequence_number < b->sequence_number;
              });
    auto before = mdat_bytes(order);

    std::vector<uint8_t> after;
    for (const OutputFile& of : rw.fragments) {
        auto boxes = parse_boxes(of.bytes);
        const Box* mdat = find_box(boxes, "mdat");
        after.insert(after.end(), of.bytes.begin() + mdat->payload_offset(),
                     of.bytes.begin() + mdat->offset + mdat->size);
    }
    REQUIRE(after == before);

    // Each output still has video traf (track 1) before audio traf (track 2).
    for (const OutputFile& of : rw.fragments) {
        auto boxes = parse_boxes(of.bytes);
        const Box* moof = find_box(boxes, "moof");
        auto trafs = find_children(*moof, "traf");
        REQUIRE(trafs.size() == 2);
        const Box* tfhd = find_child(*trafs[0], "tfhd");
        Cursor c(of.bytes, tfhd->payload_offset(),
                 tfhd->offset + tfhd->size);
        uint32_t flags = 0;
        c.fullbox(flags);
        REQUIRE(c.u32() == 1);
    }
}

TEST_CASE("rewriter normalizes tfdt onto a continuous timeline") {
    auto pkg = make_package(build_fault_set(Fault::OutOfOrder));
    auto rep = check_compatibility(pkg.index);
    auto rw = rewrite_package(pkg, rep);
    // First fragment of each track starts at decode time zero.
    for (const OutputFile& of : rw.fragments) {
        auto boxes = parse_boxes(of.bytes);
        const Box* moof = find_box(boxes, "moof");
        for (const Box* traf : find_children(*moof, "traf")) {
            const Box* tfdt = find_child(*traf, "tfdt");
            Cursor c(of.bytes, tfdt->payload_offset(),
                     tfdt->offset + tfdt->size);
            uint32_t flags = 0;
            uint8_t ver = c.fullbox(flags);
            uint64_t dts = ver == 1 ? c.u64() : c.u32();
            if (of.sequence == 1) REQUIRE(dts == 0);
        }
    }
}

TEST_CASE("rewriter refuses to write on a failed compatibility report") {
    auto pkg = make_package(build_fault_set(Fault::Duplicate));
    auto rep = check_compatibility(pkg.index);
    REQUIRE_FALSE(rep.ok);
    REQUIRE_THROWS_AS(rewrite_package(pkg, rep), RewriteError);
}

TEST_CASE("atomic writer replaces targets and is idempotent") {
    auto dir = unique_dir();
    auto pkg = make_package(build_normal_set());
    auto rep = check_compatibility(pkg.index);
    auto rw = rewrite_package(pkg, rep);

    auto first = write_package_atomic(rw, dir);
    REQUIRE(first.files.size() == 7);
    size_t total = 0;
    for (const auto& wf : first.files) total += wf.size;

    // Second run: every target already exists and is replaced.
    auto second = write_package_atomic(rw, dir);
    REQUIRE(second.files.size() == 7);
    for (const auto& wf : second.files) REQUIRE(wf.replaced_existing);

    size_t total2 = 0;
    for (const auto& wf : second.files) {
        auto p = fs::path(dir) / wf.name;
        total2 += fs::file_size(p);
    }
    REQUIRE(total == total2);
    fs::remove_all(dir);
}

TEST_CASE("atomic writer preserves existing files when verification fails") {
    auto dir = unique_dir();
    auto pkg = make_package(build_normal_set());
    auto rep = check_compatibility(pkg.index);
    auto good = rewrite_package(pkg, rep);
    write_package_atomic(good, dir);

    // Truncate an output buffer so its re-parse fails before any target is
    // touched; the previously written file must remain byte-for-byte intact.
    auto fs_path = fs::path(dir) / good.fragments[2].name;
    auto original = read_file(fs_path.string(), {});
    auto bad = good;
    bad.fragments[2].bytes.resize(4);
    REQUIRE_THROWS_AS(write_package_atomic(bad, dir), RewriteError);
    auto still = read_file(fs_path.string(), {});
    REQUIRE(still == original);
    fs::remove_all(dir);
}

TEST_CASE("empty input directory yields a structure error via load") {
    auto dir = unique_dir();
    fs::create_directories(dir);
    REQUIRE_THROWS_AS(load_package(dir), std::exception);
    fs::remove_all(dir);
}

TEST_CASE("missing initialization segment is reported") {
    auto dir = unique_dir();
    fs::create_directories(dir);
    auto set = build_normal_set();
    write_file((fs::path(dir) / set[0].first).string(), set[0].second);
    REQUIRE_THROWS_AS(load_package(dir), IndexError);
    fs::remove_all(dir);
}
