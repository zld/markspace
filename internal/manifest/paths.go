package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/media"
)

// SafeResolve resolves uri relative to sourceFile and proves the result is
// under root. It never permits absolute URIs, schemes, or ".." escapes.
func SafeResolve(root, sourceFile, uri string) (string, string, error) {
	if uri == "" {
		return "", "", &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: sourceFile, Detail: "empty URI"}
	}
	if strings.Contains(uri, "://") || strings.HasPrefix(uri, "file:") {
		return "", "", &media.Fault{Phase: media.PhaseManifest, Code: media.CodePathEscape, Source: sourceFile,
			Detail: "external URI is not allowed: " + uri}
	}
	if filepath.IsAbs(uri) || strings.HasPrefix(uri, "/") {
		return "", "", &media.Fault{Phase: media.PhaseManifest, Code: media.CodePathEscape, Source: sourceFile,
			Detail: "absolute URI is not allowed: " + uri}
	}
	cleanURI := filepath.Clean(filepath.FromSlash(uri))
	if cleanURI == ".." || strings.HasPrefix(cleanURI, ".."+string(filepath.Separator)) {
		return "", "", &media.Fault{Phase: media.PhaseManifest, Code: media.CodePathEscape, Source: sourceFile,
			Detail: "URI escapes sample root: " + uri}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", media.WrapFault(media.PhaseManifest, media.CodeInvalidArgument, root, err)
	}
	sourceAbs, err := filepath.Abs(sourceFile)
	if err != nil {
		return "", "", media.WrapFault(media.PhaseManifest, media.CodeInvalidArgument, sourceFile, err)
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(sourceAbs), cleanURI))
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", &media.Fault{
			Phase: media.PhaseManifest, Code: media.CodePathEscape,
			Source: sourceFile, Detail: fmt.Sprintf("reference %q resolves to %s outside %s", uri, target, rootAbs),
		}
	}
	return target, filepath.ToSlash(rel), nil
}

func readLimited(path string, limits Limits) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: path, Err: err}
	}
	max := limits.MaxBytes
	if max <= 0 {
		max = media.MaxInputBytes
	}
	if st.Size() > max {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeSizeLimit, Source: path,
			Offset: st.Size(), Detail: fmt.Sprintf("size %d exceeds %d", st.Size(), max)}
	}
	return os.ReadFile(path)
}

type refTracker map[string]string

func (r refTracker) check(path, source string, line int) error {
	if prior, ok := r[path]; ok {
		return &media.Fault{Phase: media.PhaseManifest, Code: media.CodeDuplicateRef,
			Source: source, Line: line, Detail: "duplicate reference to " + path + " first seen at " + prior}
	}
	r[path] = fmt.Sprintf("%s:%d", source, line)
	return nil
}
