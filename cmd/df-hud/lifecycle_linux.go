//go:build linux

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

func watchReloadSignal(ctx context.Context, reload func()) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			log.Print("config: SIGHUP")
			reload()
		}
	}
}
