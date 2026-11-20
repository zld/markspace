#include "fmp4/fixture.hpp"

#include <filesystem>

#include "fmp4/box.hpp"

namespace fmp4 {

void write_fixture(const std::string& out_dir) {
  std::error_code ec;
  std::filesystem::create_directories(out_dir, ec);
  if (ec) throw Fmp4Error(kExitUsage, "cannot create dir: " + out_dir);
  // Segment generation is added in a follow-up change.
}

}  // namespace fmp4
