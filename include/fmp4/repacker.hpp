#pragma once
// Repacker: streams fragments in manifest order, rewrites mfhd sequence
// numbers to a contiguous run while preserving every other byte (mdat and
// unknown boxes included). Output lands in a temp directory, is re-indexed
// and re-validated, and is only atomically renamed into place on success.
#include <string>
#include <vector>

#include "fmp4/fragment_index.hpp"
#include "fmp4/validator.hpp"

namespace fmp4 {

// Carries the process exit code: 3 structural/validation failure,
// 4 output commit failure. Parameter errors stay plain Fmp4Error (2).
class RepackError : public Fmp4Error {
public:
  RepackError(int code, const std::string &msg, std::string f = "",
              uint64_t off = 0)
      : Fmp4Error(msg, std::move(f), off), code_(code) {}
  int exit_code() const noexcept { return code_; }
  std::vector<uint32_t> missing_sequences;

private:
  int code_;
};

struct RepackResult {
  uint32_t input_fragment_count = 0;
  uint32_t output_fragment_count = 0;
  std::vector<uint32_t> mfhd_before;
  std::vector<uint32_t> mfhd_after;
  ValidationReport verification; // output vs input baseline
  std::string output_dir;
};

// Repack one manifest directory into a fresh output directory. The output
// path must not already exist. Throws Fmp4Error (parameter problems),
// RepackError (validation/commit problems) or propagates Fmp4Error from the
// parser (corrupt input). On failure no partial output remains.
RepackResult repack_directory(const std::string &input_dir,
                              const std::string &output_dir);

} // namespace fmp4
