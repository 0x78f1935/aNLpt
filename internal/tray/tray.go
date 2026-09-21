// Package tray is the small icon next to the clock, which is all of aNLpt there is to see
// while it is doing its job.
package tray

import (
	_ "embed"
	"runtime"

	"fyne.io/systray"
)

//go:embed icon.png
var iconPNG []byte

//go:embed icon.ico
var iconICO []byte

// Texts are what the menu says, in the member's language.
type Texts struct {
	Open, Pause, Resume, Quit string
}

// Options is what the icon can do.
type Options struct {
	Texts   func() Texts
	Tooltip func() string
	Paused  func() bool
	OnOpen  func()
	OnPause func(paused bool)
	OnQuit  func()
	// Tick is called now and then, so the tooltip and the menu follow what is going on.
	Tick <-chan struct{}
}

// Run shows the icon and does not return until Quit is chosen. It has to be called from
// the main goroutine: that is where an operating system wants its windows.
func Run(options Options) {
	systray.Run(func() {
		if runtime.GOOS == "windows" {
			systray.SetIcon(iconICO)
		} else {
			systray.SetIcon(iconPNG)
		}
		systray.SetTitle("")
		texts := options.Texts()
		open := systray.AddMenuItem(texts.Open, "")
		pause := systray.AddMenuItem(texts.Pause, "")
		systray.AddSeparator()
		quit := systray.AddMenuItem(texts.Quit, "")

		refresh := func() {
			texts := options.Texts()
			open.SetTitle(texts.Open)
			quit.SetTitle(texts.Quit)
			if options.Paused() {
				pause.SetTitle(texts.Resume)
			} else {
				pause.SetTitle(texts.Pause)
			}
			systray.SetTooltip(options.Tooltip())
		}
		refresh()

		go func() {
			for {
				select {
				case <-open.ClickedCh:
					options.OnOpen()
				case <-pause.ClickedCh:
					options.OnPause(!options.Paused())
					refresh()
				case <-quit.ClickedCh:
					systray.Quit()
					return
				case _, ok := <-options.Tick:
					if !ok {
						return
					}
					refresh()
				}
			}
		}()
	}, options.OnQuit)
}
