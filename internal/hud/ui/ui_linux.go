//go:build linux && cgo && !nolayershell

package ui

import (
	"context"

	"df-hud/internal/hud/gtk"
)

// Run starts the GTK4 layer-shell frontend on Linux.
func Run(ctx context.Context, deps Dependencies) error {
	return gtk.Run(ctx, gtk.Dependencies{
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
