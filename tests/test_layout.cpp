#include "test_cases.hpp"

#include <cstdio>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <unistd.h>

#include "fixture_builder.hpp"
#include "layout_engine.hpp"
#include "pipeline.hpp"
#include "renderer.hpp"
#include "style_parser.hpp"
#include "tag_resolver.hpp"
#include "test_harness.hpp"

namespace asspipe::test {

namespace fs = std::filesystem;

namespace {

std::string unique_dir(const char* tag) {
    static int counter = 0;
    auto path = fs::temp_directory_path() /
                ("asspipe_" + std::string(tag) + "_" +
                 std::to_string(static_cast<long>(::getpid())) + "_" +
                 std::to_string(++counter));
    return path.string();
}

void test_sha_and_png() {
    std::puts("[render] SHA-256 known vector and PNG structural validity");
    std::vector<std::uint8_t> abc{'a', 'b', 'c'};
    CHECK_STR(sha256_hex(abc) ==
                  "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61"
                  "f20015ad",
              sha256_hex(abc));
    std::vector<std::uint8_t> img(8 * 5 * 4, 128);
    std::vector<std::uint8_t> png;
    CHECK(encode_png(img, 8, 5, png), static_cast<int>(png.size()));
    static const std::uint8_t kSig[8] = {0x89, 'P', 'N', 'G',
                                         0x0D, 0x0A, 0x1A, 0x0A};
    bool signature_ok = png.size() >= 8;
    for (int i = 0; signature_ok && i < 8; ++i)
        signature_ok = png[i] == kSig[i];
    CHECK(signature_ok, 0);
    // IEND chunk must terminate the file.
    std::string tail(png.end() - 8, png.end());
    CHECK(tail.find("IEND") != std::string::npos, 0);
    CHECK(sha256_hex(png).size() == 64, 0);
}

void test_layout_regions() {
    std::puts("[layout] every rendered event yields a positive bbox");
    auto doc = asspipe::parse_ass(build_demo_ass(), "demo.ass");
    asspipe::build_inheritance_graph(doc);
    LayoutEngine engine(LayoutConfig{Canvas{1280, 720}, ""});
    int rendered = 0;
    for (const auto& ev : doc.events) {
        RenderedEvent out;
        std::string err;
        std::vector<Diagnostic> diags;
        bool ok = engine.render_event(doc, ev, out, err, diags);
        CHECK(ok, ev.index);
        if (!ok) continue;
        CHECK(out.width > 0 && out.height > 0, out.width);
        CHECK(out.x >= 0 && out.y >= 0, out.x);
        CHECK(out.x + out.width <= 1280 && out.y + out.height <= 720,
              out.x + out.width);
        CHECK(out.rgba.size() == 1280u * 720u * 4u,
              static_cast<int>(out.rgba.size()));
        ++rendered;
    }
    // Only events on valid styles render; all 12 fixture events do.
    CHECK(rendered == 12, rendered);
}

void test_positioning_tag() {
    std::puts("[layout] \\pos and \\an drive the returned coordinates");
    const char* src =
        "[V4+ Styles]\n"
        "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, "
        "OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, "
        "ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, "
        "Alignment, MarginL, MarginR, MarginV, Encoding\n"
        "Style: S,Arial,40,&H00FFFFFF,&H000000FF,&H00000000,"
        "&H64000000,0,0,0,0,100,100,0,0,1,2,1,2,40,40,40,1\n"
        "[Events]\n"
        "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, "
        "Effect, Text\n"
        "Dialogue: 0,0:00:00.00,0:00:01.00,S,,0,0,0,,"
        "{\\an7\\pos(100,100)}anchored\n";
    auto doc = asspipe::parse_ass(src, "pos.ass");
    asspipe::build_inheritance_graph(doc);
    LayoutEngine engine(LayoutConfig{Canvas{640, 360}, ""});
    RenderedEvent out;
    std::string err;
    std::vector<Diagnostic> diags;
    CHECK(engine.render_event(doc, doc.events.front(), out, err, diags), 0);
    // Top-left anchored text starts near the requested anchor.
    CHECK(out.x <= 110 && out.y <= 110, out.x);
}

void test_invalid_style_blocked() {
    std::puts("[layout] cyclic/missing styles never reach libass");
    auto doc = asspipe::parse_ass(build_demo_ass(), "demo.ass");
    asspipe::build_inheritance_graph(doc);
    LayoutEngine engine(LayoutConfig{Canvas{640, 360}, ""});
    // Craft an event bound to the cyclic style.
    Event ev = doc.events.front();
    ev.style_name = "LoopA";
    RenderedEvent out;
    std::string err;
    std::vector<Diagnostic> diags;
    CHECK(!engine.render_event(doc, ev, out, err, diags), 0);
    CHECK(!diags.empty() &&
              diags.front().kind == DiagKind::RenderFailure,
          0);
}

void test_demo_pipeline_outputs() {
    std::puts("[pipeline] demo writes PNGs + manifest with exact counters");
    auto dir = unique_dir("demo");
    PipelineOptions opts;
    opts.command = "demo";
    opts.output_dir = dir;
    opts.json_path = dir + "/summary.json";
    opts.canvas = Canvas{1280, 720};
    auto result = run_pipeline(opts);
    CHECK(result.exit_code == EXIT_OK, result.exit_code);
    CHECK(result.styles == 6, result.styles);
    CHECK(result.events == 12, result.events);
    CHECK(result.inheritance_errors == 2, result.inheritance_errors);
    CHECK(result.inline_tags == 48, result.inline_tags);
    CHECK(result.unknown_tags == 1, result.unknown_tags);
    CHECK(result.rendered == 12, result.rendered);
    CHECK(result.records.size() == 12,
          static_cast<int>(result.records.size()));
    int files = 0;
    for (const auto& e : fs::directory_iterator(dir))
        if (e.path().extension() == ".png") ++files;
    CHECK(files == 12, files);
    for (const auto& rec : result.records) {
        CHECK(rec.width > 0 && rec.height > 0, rec.event_id);
        CHECK(rec.sha256.size() == 64, rec.event_id);
        CHECK(fs::exists(rec.png_path), rec.event_id);
    }
    std::ifstream jf(opts.json_path);
    std::stringstream ss;
    ss << jf.rdbuf();
    std::string manifest = ss.str();
    CHECK(manifest.find("\"sha256\"") != std::string::npos, 0);
    CHECK(manifest.find("inheritance_errors") != std::string::npos, 0);
    CHECK(render_json(result).find("\"records\"") != std::string::npos, 0);
}

void test_exit_codes_and_limits() {
    std::puts("[pipeline] exit code 2 for bad input, 3 for render failure");
    auto dir = unique_dir("bad");
    const std::string input = dir + "/in.ass";
    fs::create_directories(dir);
    std::ofstream(input) <<
        "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, "
        "MarginR, MarginV, Effect, Text\n"
        "Dialogue: 0,nope,0:00:01.00,S,,0,0,0,,text\n";
    PipelineOptions bad;
    bad.command = "render";
    bad.input_path = input;
    bad.output_dir = dir + "/out";
    bad.json_path = dir + "/out/s.json";
    bad.canvas = Canvas{320, 240};
    auto r1 = run_pipeline(bad);
    CHECK(r1.exit_code == EXIT_INVALID_INPUT, r1.exit_code);
    CHECK(fs::exists(bad.json_path), 0);

    // Path collision guard.
    PipelineOptions collision = bad;
    collision.output_dir = input; // output must not equal input file
    collision.json_path = dir + "/c.json";
    auto r2 = run_pipeline(collision);
    CHECK(r2.exit_code == EXIT_INVALID_INPUT, r2.exit_code);

    // Oversized input (> 32 MiB) is rejected without allocation blow-up.
    const std::string big = dir + "/big.ass";
    {
        std::ofstream f(big, std::ios::binary);
        std::string chunk(1024 * 1024, 'x');
        for (int i = 0; i < 33; ++i) f << chunk;
    }
    PipelineOptions over;
    over.command = "render";
    over.input_path = big;
    over.output_dir = dir + "/bigout";
    over.json_path = dir + "/bigout/s.json";
    over.canvas = Canvas{320, 240};
    auto r3 = run_pipeline(over);
    CHECK(r3.exit_code == EXIT_INVALID_INPUT, r3.exit_code);
}

void test_robustness_garbage() {
    std::puts("[pipeline] malformed/garbage input never crashes");
    const std::string junk[] = {
        "",
        "}}} {{{ \x01\x02 not ass at all",
        "[Unclosed Section\nStyle: ,,,,,,,,,,,,,,,,,,,,,,,\n",
        "Dialogue: only, few, fields\n",
        std::string(4096, '{') + std::string(4096, '\\') + "end",
    };
    for (const auto& text : junk) {
        auto doc = asspipe::parse_ass(text, "junk");
        asspipe::build_inheritance_graph(doc);
        for (auto& ev : doc.events) {
            asspipe::TagCounts c;
            bool uc = false;
            std::string vis;
            asspipe::resolve_tags(ev.text, c, uc, vis); // must not throw
        }
    }
    CHECK(true, 0);
}

} // namespace

void run_layout_tests() {
    test_sha_and_png();
    test_layout_regions();
    test_positioning_tag();
    test_invalid_style_blocked();
    test_demo_pipeline_outputs();
    test_exit_codes_and_limits();
    test_robustness_garbage();
}

} // namespace asspipe::test
