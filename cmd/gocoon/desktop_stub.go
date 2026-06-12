//go:build !desktop

package main

import "errors"

func runDesktopWindow(_ string) error {
	return errors.New("desktop window support is not built in; rebuild with -tags desktop")
}
