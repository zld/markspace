#include "rewriter.hpp"
namespace mp4 {
WriteOutcome write_package_atomic(const RewriteResult&, const std::string&,
                                  const Limits&) {
    throw RewriteError("writer not yet available");
}
}  // namespace mp4
