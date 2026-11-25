// Command oneforarr runs the app: Movies, Shows and Indexers in one program,
// sending downloads to qBittorrent.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/accounts"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/config"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/discover"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/mediacheck"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/movies"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/scan"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/schedule"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/settings"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/shows"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/watchdog"
	"github.com/Nicho-Dev2/homey/oneforarr/app/frontend"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) > 1 && os.Args[1] == "repath" {
		os.Exit(repath(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "checksync" {
		os.Exit(checksync(os.Args[2:]))
	}

	cfg := config.FromEnv()
	db, err := store.Open(cfg.DataFile())
	if err != nil {
		log.Fatalf("Can't start: %v", err)
	}

	idx := indexers.New(db, cfg.DefinitionsDir)
	mov := movies.New(db, idx)
	tv := shows.New(db, idx)

	// Background work every 30 seconds (see package schedule).
	ctx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	wd := watchdog.New(db)
	sched := schedule.New(db, idx, mov, tv)
	sched.Watchdog = wd
	go sched.Run(ctx)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           routes(db, idx, mov, tv, wd, discover.New(db, filepath.Join(cfg.ConfigDir, "discover-cache.json"))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Printf("OneForArr %s listening on port %s (data: %s, definitions: %s)", version, cfg.Port, cfg.DataFile(), cfg.DefinitionsDir)
		if db.Read().Owner == nil {
			log.Printf("No owner yet: open the app in a browser now and create the owner account.")
		}
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Web server stopped: %v", err)
		}
	}()

	// Stop cleanly on `docker stop` (SIGTERM) or Ctrl-C.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	<-stop
	log.Printf("Stopping")
	stopWork()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

// routes wires every part of the app to its addresses.
func routes(db *store.Store, idx *indexers.Service, mov *movies.Service, tv *shows.Service, wd *watchdog.Watchdog, disc *discover.Service) http.Handler {
	accts := accounts.New(db)
	sets := &settings.Service{Store: db}

	// Routes that need a signed-in owner.
	private := http.NewServeMux()
	sets.Routes(private)
	idx.Routes(private)
	mov.Routes(private)
	tv.Routes(private)
	scan.New(db, mov, tv).Routes(private)
	wd.Routes(private)
	disc.Routes(private)
	private.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		items := append(mov.ActivityItems(), tv.ActivityItems()...)
		sort.Slice(items, func(i, j int) bool { return items[i].Download.SentAt.After(items[j].Download.SentAt) })
		webserver.JSON(w, http.StatusOK, items)
	})

	api := http.NewServeMux()
	accts.PublicRoutes(api)
	api.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		webserver.JSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})
	api.Handle("/api/", accts.RequireOwner(private))

	root := http.NewServeMux()
	root.Handle("/api/", webserver.Logged(webserver.JSONOnly(api)))
	root.Handle("/", webserver.Static(frontend.Files()))
	return root
}

// healthcheck is for Docker's HEALTHCHECK: the image has no shell or curl,
// so the program checks itself.
func healthcheck() int {
	port := config.FromEnv().Port
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// repath is "oneforarr repath FROM TO": it changes every saved path that
// starts with FROM to start with TO (see store.Data.Repath). Run it with the
// app stopped, e.g. once when moving from `make run` on the Mac into Docker:
//
//	docker compose run --rm oneforarr repath /Volumes/MyDrive/Media /data
func repath(args []string) int {
	if len(args) != 2 || !strings.HasPrefix(args[0], "/") || !strings.HasPrefix(args[1], "/") {
		fmt.Fprintln(os.Stderr, "usage: oneforarr repath /old/media/folder /new/media/folder")
		return 2
	}
	cfg := config.FromEnv()
	db, err := store.Open(cfg.DataFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	n := 0
	err = db.Update(func(d *store.Data) error {
		n = d.Repath(args[0], args[1])
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Changed %d saved paths from %s to %s in %s.\n", n, args[0], args[1], cfg.DataFile())
	return 0
}

// checksync is "oneforarr checksync FILE-OR-FOLDER...": it checks video
// files already in the library for sound out of sync, the same check every
// download gets. In Docker, the media folder is /data:
//
//	docker compose run --rm oneforarr checksync "/data/tv/Some Show (2020)"
func checksync(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: oneforarr checksync FILE-OR-FOLDER...")
		return 2
	}
	bad := 0
	for _, arg := range args {
		files := library.VideoFiles(arg)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		if len(files) == 0 {
			fmt.Printf("no video files in %s\n", arg)
		}
		for _, f := range files {
			l, err := mediacheck.Read(f.Path)
			switch {
			case errors.Is(err, mediacheck.ErrUnknown):
				fmt.Printf("%-13s %s (it doesn't record its track lengths)\n", "can't check", f.Path)
			case err != nil:
				fmt.Printf("%-13s %s (%v)\n", "can't check", f.Path, err)
			case l.OutOfSync():
				bad++
				fmt.Printf("%-13s %s: %s\n", "OUT OF SYNC", f.Path, l.Describe())
			default:
				fmt.Printf("%-13s %s\n", fmt.Sprintf("ok (%.1f s)", l.Gap().Seconds()), f.Path)
			}
		}
	}
	if bad > 0 {
		fmt.Printf("%d file(s) out of sync.\n", bad)
		return 1
	}
	return 0
}
