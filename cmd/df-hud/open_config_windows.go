//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	configShell32       = windows.NewLazySystemDLL("shell32.dll")
	configShellExecuteW = configShell32.NewProc("ShellExecuteW")
)

func platformOpenConfigAction(path func() string) func() error {
	return func() error {
		value := path()
		file, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		open, _ := windows.UTF16PtrFromString("open")
		const showNormal = 1
		result, _, _ := configShellExecuteW.Call(
			0,
			uintptr(unsafe.Pointer(open)),
			uintptr(unsafe.Pointer(file)),
			0,
			0,
			showNormal,
		)
		if result <= 32 {
			return fmt.Errorf("ShellExecuteW(%s) failed with code %d", value, result)
		}
		return nil
	}
}
