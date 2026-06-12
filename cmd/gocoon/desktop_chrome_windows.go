//go:build desktop && windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	procGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procShowWindow        = user32.NewProc("ShowWindow")
	procIsZoomed          = user32.NewProc("IsZoomed")
	procReleaseCapture    = user32.NewProc("ReleaseCapture")
	procSendMessageW      = user32.NewProc("SendMessageW")
)

const (
	gwlStyle = ^uintptr(15) // -16

	wsCaption = 0x00c00000

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020

	swMinimize = 6
	swMaximize = 3
	swRestore  = 9

	wmNCLButtonDown = 0x00a1
	htCaption       = 2
)

func configureDesktopChrome(window unsafe.Pointer) error {
	hwnd := uintptr(window)
	if hwnd == 0 {
		return fmt.Errorf("desktop: empty window handle")
	}
	style, _, err := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	if style == 0 && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("desktop: get window style: %w", err)
	}
	style &^= wsCaption
	if ret, _, err := procSetWindowLongPtrW.Call(hwnd, gwlStyle, style); ret == 0 && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("desktop: set window style: %w", err)
	}
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged)
	return nil
}

func handleDesktopWindowAction(window unsafe.Pointer, action string, terminate func()) error {
	hwnd := uintptr(window)
	if hwnd == 0 {
		return fmt.Errorf("desktop: empty window handle")
	}
	switch action {
	case "close":
		terminate()
	case "minimize":
		procShowWindow.Call(hwnd, swMinimize)
	case "toggle-maximize":
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
		} else {
			procShowWindow.Call(hwnd, swMaximize)
		}
	case "drag":
		procReleaseCapture.Call()
		procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
	}
	return nil
}
