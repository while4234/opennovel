//go:build windows

package store

import "syscall"

const windowsStillActive = 259

func revisionProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists but cannot be queried.
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(handle)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		// Be conservative when Windows cannot report the state of a process
		// that was opened successfully.
		return true
	}
	return exitCode == windowsStillActive
}
