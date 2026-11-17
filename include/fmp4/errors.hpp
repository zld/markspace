#pragma once
// Error taxonomy mapped onto the CLI exit codes:
//   Fmp4Error        -> 2, parameter / file / path errors
//   StructuralError  -> 3, malformed boxes, missing fragments, incompatibility
//   RepackError      -> 3 (validation) or 4 (output commit failure)
#include "fmp4/box_reader.hpp"

namespace fmp4 {

class StructuralError : public Fmp4Error {
public:
  explicit StructuralError(const Fmp4Error &e)
      : Fmp4Error(e.raw_message(), e.file(), e.offset()) {}
  StructuralError(const std::string &msg, std::string f = "",
                  uint64_t off = 0)
      : Fmp4Error(msg, std::move(f), off) {}
};

} // namespace fmp4
