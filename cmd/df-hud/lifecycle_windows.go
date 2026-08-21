//go:build windows

package main

import (
	"context"
	"os"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func watchReloadSignal(ctx context.Context, _ func()) {
	<-ctx.Done()
}
