//go:build windows

package store

import (
	"fmt"
	"os"
	"unsafe"
)

const validationCloneFileAttributeReparsePoint = 0x400

func validationCloneFileIdentity(path string, _ os.FileInfo) (string, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	var info publicationRootByHandleInformation
	ok, _, callErr := publicationRootGetFileInformationByHandle.Call(file.Fd(), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return "", 0, callErr
	}
	if info.FileAttributes&validationCloneFileAttributeReparsePoint != 0 {
		return "", 0, fmt.Errorf("validation clone reparse point is forbidden")
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), uint64(info.NumberOfLinks), nil
}
