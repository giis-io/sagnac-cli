package instructions

import (
	"fmt"
	"os/exec"
	"testing"
)

// exec1 returns the error of a program that exits with status 1.
func exec1(t *testing.T) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit 1").Run()
}

func exitWith(t *testing.T, code int) error {
	t.Helper()
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
}
