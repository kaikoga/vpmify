// Command vpmify-gui is a prototype of Vpmify's desktop front end, built with
// Wails v3. It calls the Go core directly instead of running the CLI.
package main

import (
	"embed"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

// scheme is the custom URL scheme that web pages use to hand a recipe to
// Vpmify with one click. build/config.yml registers the same name.
const scheme = "vpmify"

func init() {
	application.RegisterEvent[string]("url")
}

func main() {
	logger := newLogger()
	logger.Info("start", "pid", os.Getpid(), "args", os.Args)

	service, err := NewVpmifyService()
	if err != nil {
		logger.Error("init", "err", err)
		os.Exit(1)
	}

	var app *application.App
	var window *application.WebviewWindow
	receive := func(url, via string) {
		logger.Info("url", "url", url, "via", via)
		service.addURL(url)
		app.Event.Emit("url", url)
		if window != nil {
			window.Show()
			window.Restore()
			window.Focus()
		}
	}

	app = application.New(application.Options{
		Name:        "Vpmify",
		Description: "Convert UPM packages into VPM packages for ALCOM",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Windows and Linux start a new process for every URL, and so does
		// macOS for `open -n`; the second process hands its arguments over
		// here and exits.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "net.kaikoga.vpmify",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				logger.Info("second instance", "args", data.Args)
				for _, url := range schemeURLs(data.Args) {
					receive(url, "second instance")
				}
			},
		},
	})

	// macOS delivers URLs as Apple Events, to the first launch and to a
	// running instance alike, never through argv.
	app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		receive(e.Context().URL(), "apple event")
	})

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Vpmify",
		Width:  900,
		Height: 600,
		URL:    "/",
	})

	// Windows and Linux pass the URL of the first launch through argv.
	for _, url := range schemeURLs(os.Args[1:]) {
		logger.Info("url", "url", url, "via", "argv")
		service.addURL(url)
	}

	if err := app.Run(); err != nil {
		logger.Error("run", "err", err)
		os.Exit(1)
	}
}

func schemeURLs(args []string) []string {
	var urls []string
	for _, a := range args {
		if strings.HasPrefix(a, scheme+":") {
			urls = append(urls, a)
		}
	}
	return urls
}

// newLogger writes to the file named by VPMIFY_GUI_LOG, so that a run started
// by LaunchServices (which has no terminal) can be checked afterwards.
func newLogger() *slog.Logger {
	var w io.Writer = os.Stderr
	if p := os.Getenv("VPMIFY_GUI_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			w = f
		}
	}
	return slog.New(slog.NewTextHandler(w, nil))
}
