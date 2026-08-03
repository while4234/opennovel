//go:build windows

package store

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestRevisionProcessAliveRejectsExitedProcessWithRetainedHandle(t *testing.T) {
	process := exec.Command("cmd.exe", "/c", "exit 0")
	if err := process.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}

	handle, err := syscall.OpenProcess(
		syscall.PROCESS_QUERY_INFORMATION,
		false,
		uint32(process.Process.Pid),
	)
	if err != nil {
		_ = process.Process.Kill()
		_ = process.Wait()
		t.Fatalf("retain helper process handle: %v", err)
	}
	defer syscall.CloseHandle(handle)

	if err := process.Wait(); err != nil {
		t.Fatalf("wait for helper process: %v", err)
	}
	if revisionProcessAlive(process.Process.Pid) {
		t.Fatal("exited process was reported alive while its kernel object was retained")
	}
}
