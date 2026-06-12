//go:build desktop

package main

import webview "github.com/webview/webview_go"

func init() {
	desktopBuildDefault = true
}

func runDesktopWindow(url string) error {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Cocoon")
	w.SetSize(1180, 760, webview.HintNone)
	if err := configureDesktopChrome(w.Window()); err != nil {
		return err
	}
	if err := w.Bind("cocoonWindowAction", func(action string) error {
		return handleDesktopWindowAction(w.Window(), action, w.Terminate)
	}); err != nil {
		return err
	}
	w.Navigate(url)
	w.Run()
	return nil
}
