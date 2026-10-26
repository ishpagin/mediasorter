// mediasorter-desktop: the desktop app. Either the analysis runs on this computer (photos in local or NAS
// folders, models downloaded on first start), or it shows a Mediasorter server on the network.
//
// The window shows a page of a local HTTP server on 127.0.0.1 (a random port): the same Vue app and API
// as the server, plus /api/desktop/* for the connect page. Plain HTTP (not Wails' asset scheme) keeps
// video seeking (Range requests) working; in server mode the window opens the server's own address.
//
// Build: go build -tags desktop,production -ldflags "-s -w -H windowsgui" (Windows; see desktop/ for packaging).
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

func main() {
	dir, err := dataDir()
	if err != nil {
		log.Fatal(err)
	}
	if f, err := os.OpenFile(filepath.Join(dir, "mediasorter.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(f)
	}
	app, err := newApp(dir)
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	app.base = fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	go func() { log.Print(http.Serve(ln, app.handler())) }()

	m := menu.NewMenu()
	if isMac() {
		m.Append(menu.AppMenu())
	}
	app.menu = m.AddSubmenu("Mediasorter")
	app.menu.AddText("Подключение…", keys.CmdOrCtrl("k"), func(*menu.CallbackData) { app.open("/connect") })
	app.menu.AddText("Обновить страницу", keys.CmdOrCtrl("r"), func(*menu.CallbackData) { wrt.WindowExecJS(app.ctx, "location.reload()") })
	app.menu.AddText("Открыть в браузере", nil, func(*menu.CallbackData) { wrt.BrowserOpenURL(app.ctx, app.startURL()) })
	app.menu.AddSeparator()
	app.menu.AddText("Выход", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { wrt.Quit(app.ctx) })
	if isMac() {
		m.Append(menu.EditMenu()) // Cmd+C / Cmd+V in text fields
		m.Append(menu.WindowMenu())
	}

	err = wails.Run(&options.App{
		Title:     "Mediasorter",
		Width:     1440,
		Height:    920,
		MinWidth:  960,
		MinHeight: 600,
		Menu:      m,
		// the only "asset": a page that sends the window to the local server
		AssetServer: &assetserver.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><body style="background:#f7f6f3">`+
				`<script>location.replace(%q)</script>`, app.base+"/connect?start=1")
		})},
		BackgroundColour:   &options.RGBA{R: 247, G: 246, B: 243, A: 255},
		OnStartup:          func(ctx context.Context) { app.ctx = ctx },
		OnBeforeClose:      app.beforeClose,
		OnShutdown:         func(context.Context) { app.shutdown() },
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "io.github.ishpagin.mediasorter", OnSecondInstanceLaunch: func(options.SecondInstanceData) { wrt.WindowUnminimise(app.ctx); wrt.WindowShow(app.ctx) }},
	})
	if err != nil {
		log.Fatal(err)
	}
}
