//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// acquireSingleton 用全局命名互斥量保证单实例,优于 pid 文件(崩溃后不留残留)
func acquireSingleton() (release func(), ok bool) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("CreateMutexW")
	name, err := syscall.UTF16PtrFromString(`Global\larkrobotai-singleton`)
	if err != nil {
		return nil, false
	}
	handle, _, callErr := proc.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, false
	}
	const errorAlreadyExists = 183
	if errno, isErrno := callErr.(syscall.Errno); isErrno && errno == errorAlreadyExists {
		syscall.CloseHandle(syscall.Handle(handle))
		return nil, false
	}
	return func() { syscall.CloseHandle(syscall.Handle(handle)) }, true
}
