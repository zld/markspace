// subtitle-audit: command line entry point.
//
//   subtitle-audit demo --output PATH
//   subtitle-audit analyze --input PATH --summary PATH [--canvas WxH] [--jobs N]
//
// Exit codes: 0 ok, 2 input/inheritance error, 3 render error,
// 4 file error, 64 usage error.

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>

#include "document.hpp"
#include "layout.hpp"
#include "libass_renderer.hpp"
#include "style_graph.hpp"
#include "summary.hpp"
#include "tag_machine.hpp"

namespace {

constexpr int kExitOk = 0;
constexpr int kExitInput = 2;
constexpr int kExitRender = 3;
constexpr int kExitFile = 4;
constexpr int kExitUsage = 64;

void usage() {
    std::fprintf(stderr,
                 "usage:\n"
                 "  subtitle-audit demo --output PATH\n"
                 "  subtitle-audit analyze --input PATH --summary PATH"
                 " [--canvas WxH] [--jobs N]\n");
}

bool write_file(const std::string& path, const std::string& content, std::string& err) {
    std::ofstream f(path, std::ios::binary | std::ios::trunc);
    if (!f) {
        err = "cannot open '" + path + "' for writing";
        return false;
    }
    f.write(content.data(), static_cast<std::streamsize>(content.size()));
    f.flush();
    if (!f) {
        err = "failed while writing '" + path + "'";
        return false;
    }
    return true;
}

bool read_file(const std::string& path, std::string& content, std::string& err) {
    std::ifstream f(path, std::ios::binary);
    if (!f) {
        err = "cannot open '" + path + "'";
        return false;
    }
    std::ostringstream ss;
    ss << f.rdbuf();
    content = ss.str();
    return true;
}

// Fetches the value of a --flag; sets ok=false when it is missing.
std::string arg_value(int& i, int argc, char** argv, bool& ok) {
    if (i + 1 >= argc) {
        ok = false;
        return "";
    }
    return argv[++i];
}

int cmd_demo(int argc, char** argv) {
    std::string output;
    for (int i = 0; i < argc; ++i) {
        const std::string a = argv[i];
        bool ok = true;
        if (a == "--output")
            output = arg_value(i, argc, argv, ok);
        else
            ok = false;
        if (!ok) {
            usage();
            return kExitUsage;
        }
    }
    if (output.empty()) {
        usage();
        return kExitUsage;
    }
    std::string err;
    if (!write_file(output, subaudit::generate_demo_ass(), err)) {
        std::fprintf(stderr, "file: %s\n", err.c_str());
        return kExitFile;
    }
    std::printf("demo fixture written to %s\n", output.c_str());
    return kExitOk;
}

int cmd_analyze(int argc, char** argv) {
    std::string input, summary, canvas = "1280x720";
    int jobs = 1;
    for (int i = 0; i < argc; ++i) {
        const std::string a = argv[i];
        bool ok = true;
        if (a == "--input")
            input = arg_value(i, argc, argv, ok);
        else if (a == "--summary")
            summary = arg_value(i, argc, argv, ok);
        else if (a == "--canvas")
            canvas = arg_value(i, argc, argv, ok);
        else if (a == "--jobs")
            jobs = std::atoi(arg_value(i, argc, argv, ok).c_str());
        else
            ok = false;
        if (!ok) {
            usage();
            return kExitUsage;
        }
    }
    if (input.empty() || summary.empty() || jobs < 1 || jobs > 8) {
        usage();
        return kExitUsage;
    }
    int cw = 0, ch = 0, used = 0;
    if (std::sscanf(canvas.c_str(), "%dx%d%n", &cw, &ch, &used) != 2 ||
        used != static_cast<int>(canvas.size()) || cw <= 0 || ch <= 0) {
        usage();
        return kExitUsage;
    }

    std::string data, err;
    if (!read_file(input, data, err)) {
        std::fprintf(stderr, "file: %s\n", err.c_str());
        return kExitFile;
    }
    subaudit::Document doc;
    if (!subaudit::parse_ass(data, doc, err)) {
        std::fprintf(stderr, "input: %s\n", err.c_str());
        return kExitInput;
    }
    std::vector<subaudit::ResolvedStyle> styles;
    if (!subaudit::resolve_styles(doc, styles, err)) {
        std::fprintf(stderr, "inheritance: %s\n", err.c_str());
        return kExitInput;
    }
    subaudit::TagStats tags;
    for (const auto& ev : doc.events) subaudit::scan_event_text(ev.text, tags);
    std::vector<subaudit::LayoutInput> layout;
    if (!subaudit::build_layout_inputs(doc, styles, layout, err)) {
        std::fprintf(stderr, "input: %s\n", err.c_str());
        return kExitInput;
    }
    std::vector<subaudit::RenderedRegion> regions;
    if (!subaudit::render_regions(layout, cw, ch, jobs, regions, err)) {
        std::fprintf(stderr, "render: %s\n", err.c_str());
        return kExitRender;
    }
    const subaudit::AuditReport report{cw, ch, jobs, styles, tags, regions};
    if (!write_file(summary, subaudit::report_to_json(report), err)) {
        std::fprintf(stderr, "file: %s\n", err.c_str());
        return kExitFile;
    }
    subaudit::print_report(report, stdout);
    return kExitOk;
}

}  // namespace

int main(int argc, char** argv) {
    if (argc < 2) {
        usage();
        return kExitUsage;
    }
    const std::string cmd = argv[1];
    if (cmd == "demo") return cmd_demo(argc - 2, argv + 2);
    if (cmd == "analyze") return cmd_analyze(argc - 2, argv + 2);
    usage();
    return kExitUsage;
}
