//go:build windows

package main

import (
	"log"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

func main() {
	var mw *walk.MainWindow
	var urlEdit *walk.LineEdit
	var tokenEdit *walk.LineEdit
	var statusLabel *walk.Label

	err := (MainWindow{
		AssignTo: &mw,
		Title:    "WireGuard proxy",
		Font:     Font{Family: "Segoe UI", PointSize: 9},
		MinSize:  Size{Width: 520, Height: 260},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "WireGuard endpoint  " + listenAddr},
			Label{Text: "WebSocket URL"},
			LineEdit{
				AssignTo:  &urlEdit,
				CueBanner: "wss://example.com/wireguard",
			},
			Label{Text: "Authentication token"},
			LineEdit{
				AssignTo:  &tokenEdit,
				CueBanner: "Lambda MicroVM auth token",
			},
			Label{AssignTo: &statusLabel, Text: "Starting"},
			PushButton{
				Text:      "Hide to tray",
				OnClicked: func() { mw.Hide() },
			},
		},
	}).Create()
	if err != nil {
		log.Fatal(err)
	}

	setStatus := func(text string) {
		mw.Synchronize(func() {
			_ = statusLabel.SetText(text)
		})
	}

	br, err := newBridge(listenAddr, setStatus)
	if err != nil {
		_ = statusLabel.SetText(err.Error())
	} else {
		defer br.Close()
	}

	var pending *time.Timer
	schedule := func() {
		url := strings.TrimSpace(urlEdit.Text())
		token := strings.TrimSpace(tokenEdit.Text())
		if pending != nil {
			pending.Stop()
		}
		pending = time.AfterFunc(400*time.Millisecond, func() {
			if br != nil {
				br.setTarget(url, token)
			}
		})
	}
	urlEdit.TextChanged().Attach(func() { schedule() })
	tokenEdit.TextChanged().Attach(func() { schedule() })

	icon, err := walk.NewIconFromSysDLL("shell32", 0)
	if err != nil {
		log.Fatal(err)
	}

	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		log.Fatal(err)
	}
	defer ni.Dispose()

	if err := ni.SetIcon(icon); err != nil {
		log.Fatal(err)
	}
	if err := ni.SetToolTip("WireGuard proxy"); err != nil {
		log.Fatal(err)
	}

	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		mw.Show()
		mw.SetFocus()
	})

	exitAction := walk.NewAction()
	if err := exitAction.SetText("Exit"); err != nil {
		log.Fatal(err)
	}
	exitAction.Triggered().Attach(func() { walk.App().Exit(0) })
	if err := ni.ContextMenu().Actions().Add(exitAction); err != nil {
		log.Fatal(err)
	}
	if err := ni.SetVisible(true); err != nil {
		log.Fatal(err)
	}

	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		mw.Hide()
	})

	mw.Run()
}
