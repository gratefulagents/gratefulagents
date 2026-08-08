//go:build !linux

package securitytoolpacks

import "fmt"

func ExecSandboxWithLimits(_ []string) error {
	return fmt.Errorf("OCI-root execution requires Linux process limits")
}
