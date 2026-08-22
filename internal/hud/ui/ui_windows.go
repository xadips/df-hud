//go:build windows && !nolayershell

package ui

import (
	"context"

	ebitenhud "df-hud/internal/hud/ebiten"
)

// Run starts the native cgo-free Windows frontend.
func Run(ctx context.Context, deps Dependencies) error {
	return ebitenhud.Run(ctx, ebitenhud.Dependencies{
		Config:             deps.Config,
		Derive:             deps.Derive,
		MaybeSave:          deps.MaybeSave,
		Visibility:         deps.Visibility,
		OnVisibilityChange: deps.OnVisibilityChange,
		GroupHidden:        deps.GroupHidden,
		OnGroupsChange:     deps.OnGroupsChange,
		OnConfigReload:     deps.OnConfigReload,
	})
}
