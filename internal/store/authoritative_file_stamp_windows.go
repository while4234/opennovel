//go:build windows

package store

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

var getFileInformationByHandleEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

func authoritativeFileChangeStamp(path string, _ os.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var info fileBasicInfo
	ok, _, callErr := getFileInformationByHandleEx.Call(file.Fd(), 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		return "", callErr
	}
	return fmt.Sprintf("%x", uint64(info.ChangeTime)), nil
}
