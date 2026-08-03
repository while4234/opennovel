//go:build windows

package store

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type publicationRootByHandleInformation struct {
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

var publicationRootGetFileInformationByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandle")

func publicationRootFileIdentity(path string, _ os.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var info publicationRootByHandleInformation
	ok, _, callErr := publicationRootGetFileInformationByHandle.Call(file.Fd(), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return "", fmt.Errorf("read publication root file identity: %w", callErr)
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
