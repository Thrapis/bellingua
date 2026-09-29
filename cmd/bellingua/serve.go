package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/Thrapis/bellingua/internal/api"
	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/mt"
	"github.com/Thrapis/bellingua/internal/mtserver"
	"github.com/Thrapis/bellingua/internal/ui"
)

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	load := commonFlags(fs)
	listen := fs.String("listen", "", "address to listen on (overrides config)")
	open := fs.Bool("open", false, "open the editor in the default browser")
	fs.Parse(args)
	e, err := load()
	if err != nil {
		return err
	}
	if *listen != "" {
		e.cfg.Listen = *listen
	}
	st, err := openStore(ctx, e)
	if err != nil {
		return err
	}
	defer st.Close()

	chain, err := mt.New(e.cfg.MT)
	if err != nil {
		return err
	}
	bm, err := backup.NewManager(st, e.cfg.Backup, e.log)
	if err != nil {
		return err
	}
	defer bm.Close()

	// Jobs get their own context so shutdown can cancel them after the HTTP
	// server has stopped accepting requests.
	jobsCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()
	if !ui.Built() {
		e.log.Warn("web UI not embedded; build with scripts/build.ps1 (API only)")
	}
	// The Lingvanex server (third_party/lingvanex-server) starts in the
	// background: the editor is usable while it loads.
	var mtsrv *mtserver.Server
	for _, p := range e.cfg.MT.Providers {
		if p.Name == "lingvanex" {
			if mtsrv, err = mtserver.New(e.cfg.Lingvanex, p.URL, e.log); err != nil {
				return err
			}
		}
	}
	if mtsrv != nil {
		if err := mtsrv.Start(ctx); err != nil {
			e.log.Warn("machine translation unavailable", "err", err)
		}
		defer mtsrv.Stop()
	}

	srv := api.New(jobsCtx, st, e.cfg, chain, bm, ui.Dist(), e.log)
	if mtsrv != nil {
		srv.SetMTStatus(mtsrv.Status)
	}

	ln, err := net.Listen("tcp", e.cfg.Listen)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := "http://" + ln.Addr().String()
	e.log.Info("bellingua ready", "url", url, "db", e.cfg.DB)
	if *open {
		openBrowser(url)
	}

	backupDone := make(chan struct{})
	go func() { bm.Loop(ctx); close(backupDone) }()
	go idleMaintenance(ctx, st.Optimize)

	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	e.log.Info("shutting down")
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hs.Shutdown(shut)
	cancelJobs()
	<-backupDone // final backup when there were edits
	return nil
}

// idleMaintenance runs PRAGMA optimize and a WAL checkpoint periodically.
func idleMaintenance(ctx context.Context, optimize func(context.Context)) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			optimize(ctx)
		}
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func cmdBackup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	load := commonFlags(fs)
	list := fs.Bool("list", false, "list snapshots on every target instead of taking one")
	fs.Parse(args)
	e, err := load()
	if err != nil {
		return err
	}
	targets, err := backup.Targets(e.cfg.Backup)
	if err != nil {
		return err
	}
	defer func() {
		for _, t := range targets {
			t.Close()
		}
	}()
	if *list {
		for _, t := range targets {
			names, err := backup.List(ctx, t)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name(), err)
			}
			fmt.Println(t.Name())
			for _, n := range names {
				fmt.Println("  " + n)
			}
		}
		return nil
	}
	if _, err := os.Stat(e.cfg.DB); err != nil {
		return err
	}
	res, err := backup.Run(ctx, e.cfg.DB, targets, e.cfg.Backup.Keep)
	if err != nil {
		return err
	}
	e.log.Info("backup done", "name", res.Name, "size", res.Size, "took", res.Duration.Round(time.Millisecond))
	return nil
}

func cmdRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	load := commonFlags(fs)
	from := fs.String("from", "latest", `snapshot name, or "latest"`)
	target := fs.String("target", "", "target to restore from: dir | sftp (default: sftp if configured)")
	fs.Parse(args)
	e, err := load()
	if err != nil {
		return err
	}
	targets, err := backup.Targets(e.cfg.Backup)
	if err != nil {
		return err
	}
	defer func() {
		for _, t := range targets {
			t.Close()
		}
	}()
	var t backup.Target
	for _, c := range targets {
		_, isSFTP := c.(*backup.SFTP)
		switch {
		case *target == "sftp" && isSFTP, *target == "dir" && !isSFTP:
			t = c
		case *target == "" && (t == nil || isSFTP):
			t = c
		}
	}
	if t == nil {
		return errors.New("no matching backup target configured")
	}
	if err := backup.Restore(ctx, t, *from, e.cfg.DB); err != nil {
		return err
	}
	e.log.Info("restored", "from", t.Name(), "snapshot", *from, "db", e.cfg.DB, "previous", e.cfg.DB+".bak")
	return nil
}
