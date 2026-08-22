//go:build windows && !nolayershell

package ebitenhud

import (
	"df-hud/internal/config"
	"df-hud/internal/model"
	"time"
)

// Dependencies is the presentation-only seam used by the native Windows HUD.
type Dependencies struct {
	Config             func() *config.Config
	Derive             func(time.Time) *model.View
	MaybeSave          func() error
	Visibility         func() model.Visibility
	OnVisibilityChange func(func(model.Visibility))
	GroupHidden        func(string) bool
	OnGroupsChange     func(func())
	OnConfigReload     func(func(*config.Config))
}

func (d Dependencies) valid() bool {
	return d.Config != nil && d.Derive != nil && d.MaybeSave != nil &&
		d.Visibility != nil && d.OnVisibilityChange != nil &&
		d.GroupHidden != nil && d.OnGroupsChange != nil && d.OnConfigReload != nil
}
