package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "AUK",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Matches --color-app (dark theme) so there's no flash of the wrong
		// color before the webview paints on launch.
		BackgroundColour: &options.RGBA{R: 10, G: 10, B: 10, A: 1},
		// Wails' own default is a DISABLED zoom/maximize button: its macOS
		// backend only enables it when frontendOptions.Mac is non-nil (see
		// wails/v2/internal/frontend/desktop/darwin/window.go) — leaving Mac
		// unset here (as this app did before) silently disabled the green
		// traffic-light button. mac.Options{} alone (DisableZoom defaults to
		// false) is enough to fix it without changing anything else about
		// the title bar.
		Mac:       &mac.Options{},
		OnStartup: app.startup,
		// Closes any live MCP client sessions (see app.shutdown) — without
		// this, a stdio-backed debugging connection's subprocess can outlive
		// the app itself.
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
