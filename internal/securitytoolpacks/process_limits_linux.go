//go:build linux

package securitytoolpacks

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// ExecSandboxWithLimits atomically applies limits in the trusted wrapper process
// before replacing it with Bubblewrap; untrusted scanner code cannot run first.
func ExecSandboxWithLimits(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("sandbox launcher requires file limit, memory limit, and argv")
	}
	fileLimit, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil || fileLimit == 0 {
		return fmt.Errorf("invalid sandbox file limit")
	}
	memoryLimit, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil || memoryLimit == 0 {
		return fmt.Errorf("invalid sandbox memory limit")
	}
	for resource, limit := range map[int]uint64{unix.RLIMIT_FSIZE: fileLimit, unix.RLIMIT_AS: memoryLimit, unix.RLIMIT_CORE: 0} {
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); err != nil {
			return err
		}
	}
	return unix.Exec(args[2], args[2:], os.Environ())
}
