//go:build linux

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openStagedRegularFile anchors lookup beneath the selected staging root and
// rejects symlinks in every path component. Workspace files can change while
// an archive is being built; a raced symlink must never widen the archive.
func openStagedRegularFile(root, path string) (*os.File, error) {
	base := root
	if info, err := os.Lstat(root); err != nil {
		return nil, err
	} else if !info.IsDir() {
		base = filepath.Dir(root)
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("staged file %q escapes target root", path)
	}
	rootFD, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD) //nolint:errcheck // best-effort cleanup of a private descriptor
	how := &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(rootFD, filepath.ToSlash(rel), how)
	if err != nil {
		return nil, fmt.Errorf("open staged file %q without following symlinks: %w", rel, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
