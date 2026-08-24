//go:build !linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func openStagedRegularFile(root, path string) (*os.File, error) {
	base := root
	if info, err := os.Lstat(root); err != nil {
		return nil, err
	} else if !info.IsDir() {
		base = filepath.Dir(root)
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(resolvedBase, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("staged file %q escapes target root", path)
	}
	return os.Open(resolved) // #nosec G304 -- resolved and checked beneath the staging root
}
