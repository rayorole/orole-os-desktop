// Command orole-os is the Orole OS desktop application (MyGo + React).
package main

import (
	"context"
	_ "embed"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/rayorole/orole-os-desktop/internal/app"
	"github.com/rayorole/orole-os-desktop/internal/paths"
)

//go:embed window-icon.png
var windowIcon []byte

func main() {
	native, primary := prepareNativeDesktop()
	if !primary {
		return
	}
	dir, err := paths.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	link := app.NewLink(dir)
	if res, err := mygo.App.Path(mygo.PathResources); err == nil {
		link.DaemonExe = filepath.Join(res, "orole-daemon.exe")
	}
	mygo.Bind(app.Daemon{L: link})
	mygo.Bind(app.Settings{L: link})
	mygo.Bind(app.Terminals{L: link})
	mygo.Bind(app.Projects{L: link})
	mygo.Bind(app.Git{L: link})
	mygo.Bind(app.Worktrees{L: link})
	mygo.Bind(app.System{L: link})
	mygo.Bind(&app.Updates{L: link})
	mygo.Bind(app.GitHub{L: link})
	mygo.Bind(app.Files{L: link})
	mygo.Bind(app.Notifications{L: link})
	mygo.Bind(app.Jarvis{L: link})
	mygo.Bind(app.Search{L: link})
	mygo.Bind(app.Shell{})
	mygo.Bind(app.Agents{L: link})
	mygo.Bind(app.Skills{L: link})
	mygo.Bind(app.Images{L: link})
	mygo.Bind(app.Automations{L: link})
	mygo.Bind(app.MCP{L: link})
	mygo.Bind(app.Policy{L: link})
	mygo.Bind(app.Plugins{L: link})
	mygo.Bind(app.Chat{L: link})
	mygo.Bind(app.LLM{L: link})
	mygo.Bind(app.Dictation{L: link})
	mygo.Bind(app.CLIRuntimes{L: link})
	mygo.Bind(app.Mail{L: link})
	mygo.Bind(app.Drive{L: link})
	mygo.Bind(app.Machines{L: link})
	mygo.Bind(app.Sandbox{L: link})
	mygo.Bind(app.Applications{L: link})
	mygo.Bind(app.Desktop{PendingNavigation: native.Navigation})

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Orole OS", Width: 1360, Height: 860, MinWidth: 960, MinHeight: 600,
			BackgroundColor: "#0f1420", StateKey: "main", URL: "/", Hidden: true,
			TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 40,
			Page: mygo.PageOptions{DevTools: devTools()},
		})
		// Show on first paint instead of an empty WebView2 frame; the timer guards a page that never reports ready.
		show := sync.OnceFunc(win.Show)
		win.OnReadyToShow(show)
		time.AfterFunc(3*time.Second, show)
		if err := win.SetIcon(windowIcon); err != nil {
			log.Printf("window icon: %v", err)
		}
		native.Attach(win)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := link.Start(ctx); err != nil {
				log.Printf("daemon link: %v", err)
			}
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
