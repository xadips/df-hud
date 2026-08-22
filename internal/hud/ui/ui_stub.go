//go:build nolayershell || (!windows && !linux) || (linux && !cgo)

package ui

import (
	"context"
	"errors"
)

// Run reports that this build intentionally has no HUD frontend.
func Run(context.Context, Dependencies) error {
	return errors.New("built with -tags nolayershell: no HUD in this binary, use -headless")
}
