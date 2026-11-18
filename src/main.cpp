#include "compatibility.hpp"
#include "fragment_index.hpp"
#include "rewriter.hpp"

#include <cstring>
#include <filesystem>
#include <iostream>
#include <sstream>

namespace fs = std::filesystem;

namespace {

constexpr int EXIT_OK = 0;
constexpr int EXIT_USAGE = 2;
constexpr int EXIT_STRUCT = 3;
constexpr int EXIT_REWRITE = 4;

struct Args {
    std::string input;
    std::string output;
    bool json = false;
    uint64_t max_samples = 1'000'000;
    uint32_t max_tracks = 64;
};

std::string json_escape(const std::string& s) {
    std::string out;
    for (char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\t': out += "\\t"; break;
            default: out += c;
        }
    }
    return out;
}

void print_human(const mp4::MediaPackage& pkg,
                 const mp4::CompatReport& rep,
                 const mp4::RewriteResult* rw,
                 const mp4::WriteOutcome* wo) {
    std::cout << "input fragments: " << pkg.index.fragments.size() << "\n";
    std::cout << "output fragments: "
              << (wo ? wo->files.size() - 1 : 0) << "\n";
    std::cout << "moof sequences (input):";
    for (const mp4::Fragment& f : pkg.index.fragments)
        std::cout << ' ' << f.sequence_number;
    std::cout << "\n";
    if (rw) {
        std::cout << "moof sequences (output):";
        for (const mp4::OutputFile& f : rw->fragments)
            std::cout << ' ' << f.sequence;
        std::cout << "\n";
    }
    if (rep.missing_fragments.empty()) {
        std::cout << "missing sequences: none\n";
    } else {
        std::cout << "missing sequences:";
        for (const auto& m : rep.missing_fragments)
            std::cout << ' ' << m.expected_sequence;
        std::cout << "\n";
    }
    std::cout << "per-track decode timeline deviation:\n";
    for (const auto& dev : rep.track_deviations)
        std::cout << "  track " << dev.track_id << ": max "
                  << dev.max_abs_deviation_ticks << " ticks over "
                  << dev.fragment_count << " fragments\n";
    for (const std::string& w : rep.warnings)
        std::cout << "warning: " << w << "\n";
    for (const std::string& e : rep.errors)
        std::cout << "error: " << e << "\n";
    std::cout << "compatibility: "
              << (rep.ok ? "PASS (repackable)" : "FAIL (write blocked)")
              << "\n";
    if (wo)
        for (const auto& f : wo->files)
            std::cout << "wrote " << f.name << " (" << f.size
                      << " bytes" << (f.replaced_existing ? ", replaced" : "")
                      << ")\n";
}

std::string build_json(const mp4::MediaPackage& pkg,
                       const mp4::CompatReport& rep,
                       const mp4::RewriteResult* rw,
                       const mp4::WriteOutcome* wo,
                       const std::string& status) {
    std::ostringstream j;
    j << "{\n  \"status\": \"" << json_escape(status) << "\",\n";
    j << "  \"input_fragment_count\": " << pkg.index.fragments.size()
      << ",\n";
    j << "  \"output_fragment_count\": "
      << (wo ? wo->files.size() - 1 : 0) << ",\n";
    j << "  \"input_sequences\": [";
    for (size_t i = 0; i < pkg.index.fragments.size(); ++i)
        j << (i ? ", " : "") << pkg.index.fragments[i].sequence_number;
    j << "],\n  \"output_sequences\": [";
    if (rw)
        for (size_t i = 0; i < rw->fragments.size(); ++i)
            j << (i ? ", " : "") << rw->fragments[i].sequence;
    j << "],\n  \"missing_sequences\": [";
    for (size_t i = 0; i < rep.missing_fragments.size(); ++i)
        j << (i ? ", " : "")
          << rep.missing_fragments[i].expected_sequence;
    j << "],\n  \"tracks\": [\n";
    for (size_t i = 0; i < rep.track_deviations.size(); ++i) {
        const auto& d = rep.track_deviations[i];
        j << "    {\"track_id\": " << d.track_id
          << ", \"max_abs_deviation_ticks\": "
          << d.max_abs_deviation_ticks
          << ", \"fragment_count\": " << d.fragment_count << "}"
          << (i + 1 < rep.track_deviations.size() ? "," : "") << "\n";
    }
    j << "  ],\n  \"warnings\": [";
    for (size_t i = 0; i < rep.warnings.size(); ++i)
        j << (i ? ", " : "") << "\"" << json_escape(rep.warnings[i]) << "\"";
    j << "],\n  \"errors\": [";
    for (size_t i = 0; i < rep.errors.size(); ++i)
        j << (i ? ", " : "") << "\"" << json_escape(rep.errors[i]) << "\"";
    j << "],\n  \"compatible\": " << (rep.ok ? "true" : "false") << ",\n";
    j << "  \"written_files\": [";
    if (wo)
        for (size_t i = 0; i < wo->files.size(); ++i)
            j << (i ? ", " : "") << "\""
              << json_escape(wo->files[i].name) << "\"";
    j << "]\n}\n";
    return j.str();
}

int usage(const char* prog) {
    std::cerr << "usage: " << prog
              << " --input DIR --output DIR [--json]"
              << " [--max-samples N] [--max-tracks N]\n";
    return EXIT_USAGE;
}

}  // namespace

int main(int argc, char** argv) {
    Args args;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--input") == 0 && i + 1 < argc)
            args.input = argv[++i];
        else if (std::strcmp(argv[i], "--output") == 0 && i + 1 < argc)
            args.output = argv[++i];
        else if (std::strcmp(argv[i], "--json") == 0)
            args.json = true;
        else if (std::strcmp(argv[i], "--max-samples") == 0 && i + 1 < argc)
            args.max_samples = std::stoull(argv[++i]);
        else if (std::strcmp(argv[i], "--max-tracks") == 0 && i + 1 < argc)
            args.max_tracks = uint32_t(std::stoul(argv[++i]));
        else
            return usage(argv[0]);
    }
    if (args.input.empty() || args.output.empty()) return usage(argv[0]);

    mp4::Limits lim;
    lim.max_samples = args.max_samples;
    lim.max_tracks = args.max_tracks;

    try {
        mp4::MediaPackage pkg = mp4::load_package(args.input, lim);
        mp4::CompatReport rep = mp4::check_compatibility(pkg.index);

        if (!rep.ok) {
            if (args.json)
                std::cout << build_json(pkg, rep, nullptr, nullptr,
                                        "incompatible") << "\n";
            else
                print_human(pkg, rep, nullptr, nullptr);
            return EXIT_STRUCT;
        }

        mp4::RewriteResult rw =
            mp4::rewrite_package(pkg, rep, {"init.mp4", "frag-", 1, lim});
        mp4::WriteOutcome wo =
            mp4::write_package_atomic(rw, args.output, lim);

        if (args.json)
            std::cout << build_json(pkg, rep, &rw, &wo, "ok") << "\n";
        else
            print_human(pkg, rep, &rw, &wo);
        return EXIT_OK;
    } catch (const mp4::FileError& e) {
        std::cerr << "file error: " << e.what() << "\n";
        return EXIT_USAGE;
    } catch (const mp4::BoxError& e) {
        std::cerr << "structure error: " << e.what() << "\n";
        return EXIT_STRUCT;
    } catch (const mp4::IndexError& e) {
        std::cerr << "compatibility error: " << e.what() << "\n";
        return EXIT_STRUCT;
    } catch (const mp4::RewriteError& e) {
        std::cerr << "rewrite error: " << e.what() << "\n";
        return EXIT_REWRITE;
    } catch (const std::exception& e) {
        std::cerr << "fatal: " << e.what() << "\n";
        return EXIT_REWRITE;
    }
}
