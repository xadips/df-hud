package gtk

import (
	"df-hud/internal/config"
	"df-hud/internal/hud/render"
	"df-hud/internal/model"
	"time"
)

const namespace = "df-hud"

type (
	Config                 = config.Config
	View                   = model.View
	hudVisibility          = model.Visibility
	Placement              = config.Placement
	BlockWidgetConfig      = config.BlockWidgetConfig
	BossesWidgetConfig     = config.BossesWidgetConfig
	SessionWidgetConfig    = config.SessionWidgetConfig
	XPWidgetConfig         = config.XPWidgetConfig
	ChallengesWidgetConfig = config.ChallengesWidgetConfig
	MapWidgetConfig        = config.MapWidgetConfig
	mapFrame               = render.MapFrame
)

var (
	theCity  = render.City()
	outposts = render.Outposts()
)

// Dependencies is the complete command-to-GTK seam. It exposes only the
// presentation-ready view and callbacks required to keep the surface current.
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
