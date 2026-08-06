package main

import (
	"embed"
	"flag"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/slam0504/sdlc-workbench/internal/approval"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-approval" {
		fs := flag.NewFlagSet("mcp-approval", flag.ExitOnError)
		sock := fs.String("socket", "", "broker unix socket path")
		_ = fs.Parse(os.Args[2:])
		if err := approval.RunMCPServer(*sock, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	runWailsApp()
}

func runWailsApp() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:  "sdlc-workbench",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
