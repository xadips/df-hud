//go:build windows

package main

import (
	"context"
	"os"
)

func newDesktopClient() desktopClient { return windowsDesktopClient{} }

func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func desktopCanStartRun(place windowPlacement) bool {
	return place.Known && place.ForegroundRule && place.OnActiveWorkspace
}

func watchReloadSignal(ctx context.Context, _ func()) {
	<-ctx.Done()
}
