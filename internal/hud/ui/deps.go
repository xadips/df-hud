// Package ui selects the HUD frontend for the current platform.
package ui

import (
	"df-hud/internal/config"
	"df-hud/internal/model"
	"time"
)

// Dependencies is the complete application-to-HUD seam.
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
