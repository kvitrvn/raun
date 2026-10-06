//go:build !unix

package agent

import "os/exec"

// setProcessGroup is a no-op where process groups are not available;
// cancellation kills the direct child only.
func setProcessGroup(*exec.Cmd) {}
