//go:build desktop && !windows

package main

import "unsafe"

func configureDesktopChrome(_ unsafe.Pointer) error {
	return nil
}

func handleDesktopWindowAction(_ unsafe.Pointer, action string, terminate func()) error {
	if action == "close" {
		terminate()
	}
	return nil
}
