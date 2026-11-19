#ifndef H264_DIAGNOSTICS_HPP
#define H264_DIAGNOSTICS_HPP

#include <cstdint>
#include <string>
#include <vector>

namespace h264 {

// One recoverable bitstream problem. Offsets are always absolute byte
// positions in the input file so reports can be checked by hand.
struct Diag {
    std::uint64_t offset;
    std::string message;
};

// Every diagnostic is treated as a parse error: strict mode fails if the
// collection is non-empty. Parsing continues after each entry so that one
// broken NAL does not hide later GOP structure.
class Diagnostics {
public:
    void add(std::uint64_t offset, const std::string& message) {
        items_.push_back(Diag{offset, message});
    }

    bool has_errors() const noexcept { return !items_.empty(); }
    std::size_t size() const noexcept { return items_.size(); }

    using const_iterator = std::vector<Diag>::const_iterator;
    const_iterator begin() const noexcept { return items_.begin(); }
    const_iterator end() const noexcept { return items_.end(); }
    const Diag& operator[](std::size_t i) const { return items_[i]; }

private:
    std::vector<Diag> items_;
};

}  // namespace h264

#endif
