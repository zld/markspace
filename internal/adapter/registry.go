package adapter

import (
	"fmt"

	"medcheck/internal/clock"
)

// Versions are the explicit, supported adapter revisions.
const (
	VersionAACV1  = "aac-v1"
	VersionH264V1 = "h264-v1"
)

// Adapter is implemented by every versioned codec/container adapter.
type Adapter interface {
	Version() string
}

// Registry resolves exact adapter versions. It never silently falls back to
// another major version.
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// NewDefaultRegistry registers the built-in v1 adapters.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	_ = r.Register(NewAACV1())
	_ = r.Register(NewH264V1())
	return r
}

// Register adds an adapter under its declared version.
func (r *Registry) Register(a Adapter) error {
	if a == nil || a.Version() == "" {
		return fmt.Errorf("adapter: cannot register nil/versionless adapter")
	}
	if _, exists := r.adapters[a.Version()]; exists {
		return fmt.Errorf("adapter: version %s already registered", a.Version())
	}
	r.adapters[a.Version()] = a
	return nil
}

// Get returns the adapter for an exact version or a comparable error.
func (r *Registry) Get(version string) (Adapter, error) {
	a, ok := r.adapters[version]
	if !ok {
		return nil, clock.MediaError{
			Code:    clock.CodeUnknownAdapterVersion,
			Message: fmt.Sprintf("adapter version %q is not registered", version),
		}
	}
	return a, nil
}

// GetAudio resolves and type-asserts an audio adapter.
func (r *Registry) GetAudio(version string) (*AACV1, error) {
	a, err := r.Get(version)
	if err != nil {
		return nil, err
	}
	out, ok := a.(*AACV1)
	if !ok {
		return nil, clock.MediaError{Code: clock.CodeUnknownAdapterVersion, Message: version + " is not an audio adapter"}
	}
	return out, nil
}

// GetVideo resolves and type-asserts a video adapter.
func (r *Registry) GetVideo(version string) (*H264V1, error) {
	a, err := r.Get(version)
	if err != nil {
		return nil, err
	}
	out, ok := a.(*H264V1)
	if !ok {
		return nil, clock.MediaError{Code: clock.CodeUnknownAdapterVersion, Message: version + " is not a video adapter"}
	}
	return out, nil
}
