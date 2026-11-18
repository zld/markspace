#pragma once

#include "compatibility.hpp"
#include "fragment_index.hpp"

#include <cstdint>
#include <string>
#include <vector>

namespace mp4 {

struct RewriteError : std::runtime_error {
    explicit RewriteError(const std::string& m) : std::runtime_error(m) {}
};

// One output file: the rewritten initialization segment or media fragment.
struct OutputFile {
    std::string name;
    std::vector<uint8_t> bytes;
    uint32_t sequence = 0;  // 0 for the initialization segment
};

struct RewriteResult {
    OutputFile init_segment;
    std::vector<OutputFile> fragments;
    // Per-track largest |rewritten tfdt - original tfdt| in ticks.
    std::map<uint32_t, uint64_t> max_abs_deviation;
    uint32_t first_sequence = 1;
};

struct RewriteOptions {
    std::string init_name = "init.mp4";
    std::string fragment_prefix = "frag-";
    uint32_t first_sequence = 1;
    Limits limits;
};

// Rebuilds every fragment with continuous sequence numbers and a continuous
// per-track decode timeline, preserving every sample byte and track order.
// Throws RewriteError on structural problems; does not touch the filesystem.
RewriteResult rewrite_package(const MediaPackage& pkg,
                              const CompatReport& report,
                              const RewriteOptions& opt = {});

// Pre-write integrity verification of a produced output file: reparses the
// bytes and compares structure against the source fragment.
bool verify_output_bytes(const std::vector<uint8_t>& bytes,
                         const Limits& lim = {});

// Serializes one box (header + original payload/children) into out.
void write_box(std::vector<uint8_t>& out, const Box& box,
               const std::vector<uint8_t>& source);

struct WrittenFile {
    std::string name;
    uint64_t size = 0;
    uint32_t sequence = 0;  // 0 for the initialization segment
    bool replaced_existing = false;
};

struct WriteOutcome {
    std::vector<WrittenFile> files;
    std::vector<std::string> removed_stale_temps;
};

// Writes every output to a temp file in the destination directory, re-reads
// and verifies each temp, and only then atomically renames it over the target.
// On failure no target is replaced; stale temps from interrupted runs are
// cleaned before writing. Safe to run repeatedly with identical results.
WriteOutcome write_package_atomic(const RewriteResult& result,
                                  const std::string& output_dir,
                                  const Limits& lim = {});

}  // namespace mp4
