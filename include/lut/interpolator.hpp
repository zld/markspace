#ifndef LUT_INTERPOLATOR_HPP
#define LUT_INTERPOLATOR_HPP

#include "lut/model.hpp"

namespace lut {

class Interpolator {
public:
    Interpolator(const Lut3D& lut, DomainPolicy policy)
        : lut_(lut), policy_(policy) {}

    // Samples the LUT at an encoded-space coordinate. With DomainPolicy::Error
    // any component outside [domainMin, domainMax] throws LutDataError;
    // Clamp saturates to the domain first. The trilinear kernel is the single
    // shared entry point for the probe and texture render paths.
    RGB sample(const RGB& position) const;

private:
    const Lut3D& lut_;
    DomainPolicy policy_;
};

}  // namespace lut

#endif  // LUT_INTERPOLATOR_HPP
