package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"stackChan/bridgego"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	binDir := filepath.Dir(executable)
	bridgeDir := filepath.Join(binDir, "..", "..", "bridge")
	if wd, err := os.Getwd(); err == nil {
		switch {
		case exists(filepath.Join(wd, "bridge")):
			bridgeDir = filepath.Join(wd, "bridge")
		case exists(filepath.Join(wd, "server", "bridge")):
			bridgeDir = filepath.Join(wd, "server", "bridge")
		}
	}
	cfg := bridgego.LoadConfig(bridgeDir, filepath.Dir(bridgeDir), filepath.Dir(filepath.Dir(bridgeDir)))
	server := bridgego.NewServer(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.ListenAndServe(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
