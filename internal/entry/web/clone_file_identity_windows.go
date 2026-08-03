//go:build windows

package web

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

type cloneByHandleFileInformation struct {
	FileAttributes     uint32
	CreationTime       syscall.Filetime
	LastAccessTime     syscall.Filetime
	LastWriteTime      syscall.Filetime
	VolumeSerialNumber uint32
	FileSizeHigh       uint32
	FileSizeLow        uint32
	NumberOfLinks      uint32
	FileIndexHigh      uint32
	FileIndexLow       uint32
}

var cloneGetFileInformationByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandle")

const cloneFileAttributeReparsePoint = 0x400

func cloneFileIdentity(path string, _ fs.FileInfo) (string, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	var info cloneByHandleFileInformation
	ok, _, callErr := cloneGetFileInformationByHandle.Call(file.Fd(), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return "", 0, callErr
	}
	if info.FileAttributes&cloneFileAttributeReparsePoint != 0 {
		return "", 0, fmt.Errorf("validation clone reparse point is forbidden")
	}
	identity := fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	return identity, uint64(info.NumberOfLinks), nil
}

func validationCloneFileGeneration(info fs.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}
