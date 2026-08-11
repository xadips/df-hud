//go:build !linux || !cgo || nolayershell

package main

import (
	"context"
	"errors"
)

// No-GUI counterpart to ui.go, selected by -tags nolayershell or when cgo is off.
// It exists so the headless core builds and tests on a machine with no GTK4 and
// no Wayland: CI, a container, or a quick check over ssh. Returning an error
// rather than silently doing nothing is what keeps "no HUD appeared" from looking
// like a bug in the HUD.
func runUI(context.Context, *app) error {
	return errors.New("built with -tags nolayershell: no HUD in this binary, use -headless")
}
