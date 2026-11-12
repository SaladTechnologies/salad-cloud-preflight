//go:build !unix

package preflight

import "os/exec"

func isolate(*exec.Cmd) {}
