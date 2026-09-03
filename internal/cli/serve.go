package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"

	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/server"
)

const serveUsage = `codewalk serve — run the local API and web UI

Usage:
  codewalk serve [flags]

The service binds to loopback and is unauthenticated, which suits a local
developer tool. Binding anywhere else requires --allow-remote.
`

func runServe(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "serve", serveUsage)
	host := fs.String("host", "", "Address to bind (default: from config, 127.0.0.1)")
	port := fs.Int("port", 0, "Port to bind (default: from config, 7457)")
	repoFlag := fs.String("repo", "", "Default repository for requests that do not name one")
	noBrowser := fs.Bool("no-browser", false, "Do not open a browser window")
	allowRemote := fs.Bool("allow-remote", false, "Allow binding outside loopback (exposes an unauthenticated API)")
	verbose := fs.Bool("verbose", false, "Log every request")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoPath := resolveRepoPath(env, *repoFlag)
	cfg, err := config.Load(repoPath)
	if err != nil {
		return err
	}
	if *host == "" {
		*host = cfg.Server.Host
	}
	if *port == 0 {
		*port = cfg.Server.Port
	}

	svc, err := newService()
	if err != nil {
		return err
	}

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level}))

	// A repository is not required to start the server, but knowing one lets
	// the UI open on something useful.
	defaultRepo := ""
	if repo, err := gitrepo.Discover(repoPath); err == nil {
		defaultRepo = repo.Root
	}

	srv, err := server.New(server.Options{
		Service:           svc,
		Config:            cfg,
		Version:           Version,
		DefaultRepository: defaultRepo,
		AllowRemote:       *allowRemote,
		Logger:            logger,
	})
	if err != nil {
		return err
	}

	url, err := srv.Listen(ctx, *host, *port)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "codewalk is serving at %s\n", url)
	if defaultRepo != "" {
		fmt.Fprintf(env.Stdout, "Repository: %s\n", defaultRepo)
	} else {
		fmt.Fprintln(env.Stdout, "No repository detected here; open one from the UI or pass --repo.")
	}
	fmt.Fprintln(env.Stdout, "Press Ctrl+C to stop.")

	if cfg.Server.OpenBrowser && !*noBrowser {
		openBrowser(url)
	}
	<-ctx.Done()
	fmt.Fprintln(env.Stdout, "\nStopped.")
	return nil
}

// openBrowser opens the UI, ignoring failures: not being able to open a browser
// is not a reason to fail a server command.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return
		}
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Start()
}

var _ = os.Getenv
