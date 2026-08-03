//go:build windows

package store

import (
	"fmt"
	"os"
	"unsafe"

	"syscall"
)

type authorityDataBlob struct {
	length uint32
	data   *byte
}

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

func protectAuthoritySecret(plaintext []byte) ([]byte, error) {
	return authorityCryptProtect(plaintext, true)
}

func unprotectAuthoritySecret(payload []byte) ([]byte, error) {
	return authorityCryptProtect(payload, false)
}

func authorityCryptProtect(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("publication authority payload is empty")
	}
	in := authorityDataBlob{length: uint32(len(input)), data: &input[0]}
	var out authorityDataBlob
	proc := procCryptUnprotectData
	if protect {
		proc = procCryptProtectData
	}
	var result uintptr
	var callErr error
	if protect {
		result, _, callErr = proc.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	} else {
		result, _, callErr = proc.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	}
	if result == 0 {
		return nil, callErr
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.data)))
	return append([]byte(nil), unsafe.Slice(out.data, int(out.length))...), nil
}

// DPAPI protects bytes before their first write. chmod is retained as a
// defense-in-depth hint; confidentiality does not depend on Windows modes.
func restrictAuthorityFile(path string) error { return os.Chmod(path, 0o600) }

func verifyProtectedAuthorityFile(path string) error {
	_, err := os.Stat(path)
	return err
}
