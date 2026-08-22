package scene

import (
	"df-hud/internal/config"
	"df-hud/internal/hud/render"
	"df-hud/internal/model"
	"math"
	"strings"
	"testing"
	"time"
)

func TestBuildTextGroupsPreservesOrderVisibilityPlacementAndStateStyles(t *testing.T) {
	cfg := config.Default()
	cfg.HUD.MarginLeft = 10
	cfg.HUD.MarginTop = 20
	cfg.HUD.Opacity = 0.5
	cfg.Widget.Block.X = 5
	cfg.Widget.Block.Y = 7
	cfg.Widget.Block.Color = "#123456"
	cfg.Widget.Challenges.Enabled = false
	cfg.Widget.Map.Enabled = false

	now := time.Unix(10_000, 0)
	view := &model.View{
		Now:         now,
		HaveData:    true,
		HasPosition: true,
		PositionX:   1000,
		PositionY:   1000,
		ZoneName:    "Dallbow",
		GameRunning: true,
		HasSession:  true,
		SessionTime: 5 * time.Minute,
		XPAvailable: true,
		XPPerHour:   12_345,
		XPStability: model.XPUnstable,
		Status:      "bridge needs attention",
		StatusIsFix: true,
		BlockEvents: []model.CityEvent{{
			Kind: model.EventSpawn, Enemies: []string{"Titan"}, End: now.Add(5 * time.Minute),
		}},
	}

	groups := BuildTextGroups(view, cfg, func(name string) bool { return name == "session" })
	if got, want := groupNames(groups), "status,block,bosses,xp"; got != want {
		t.Fatalf("group order/visibility = %q, want %q", got, want)
	}

	status := groups[0].Rows[0].Runs[0]
	if status.Style.Color != colorWarning {
		t.Errorf("fixable status color = %q, want %q", status.Style.Color, colorWarning)
	}

	block := groups[1]
	if block.Position != (Point{X: 15, Y: 27}) {
		t.Errorf("block position = %+v, want margin-adjusted 15,27", block.Position)
	}
	if got := block.Rows[0].Runs[0].Style; got.Color != "#123456" || got.Opacity != 0.5 {
		t.Errorf("block style = %+v, want configured color and global opacity", got)
	} else if len(got.Shadows) != 1 || got.Shadows[0].Blur != 2 {
		t.Errorf("block shadows = %+v, want one thin two-pixel source blur", got.Shadows)
	}

	boss := groups[2].Rows[0].Runs[0]
	if boss.Style.Color != colorWarning {
		t.Errorf("threat color = %q, want %q", boss.Style.Color, colorWarning)
	}

	xp := groups[3].Rows[0].Runs[0]
	if xp.Style.Color != colorAlarm {
		t.Errorf("unstable XP color = %q, want %q", xp.Style.Color, colorAlarm)
	}
}

func TestChallengeRowsUseExplicitRunsWithoutPangoMarkup(t *testing.T) {
	cfg := config.Default()
	cfg.HUD.Opacity = 0.8
	cfg.Widget.Block.Enabled = false
	cfg.Widget.Bosses.Enabled = false
	cfg.Widget.Session.Enabled = false
	cfg.Widget.XP.Enabled = false
	cfg.Widget.Map.Enabled = false
	cfg.Widget.Challenges.ShowCompleted = true

	now := time.Unix(20_000, 0)
	view := &model.View{
		Now: now,
		Challenges: []model.Challenge{{
			Name: "Weekly Hunt",
			End:  now.Add(2 * time.Hour),
			Objectives: []model.Objective{{
				Name: "Kill Zombies", Score: 10, Target: 10, HasScore: true,
			}},
		}},
	}

	groups := BuildTextGroups(view, cfg, nil)
	if len(groups) != 1 || groups[0].Name != "challenges" {
		t.Fatalf("groups = %+v, want only challenges", groupNames(groups))
	}
	if len(groups[0].Rows) != 2 {
		t.Fatalf("challenge rows = %d, want heading and objective", len(groups[0].Rows))
	}

	for rowIndex, row := range groups[0].Rows {
		for _, run := range row.Runs {
			if strings.ContainsAny(run.Text, "<>") {
				t.Errorf("row %d contains markup-like text %q", rowIndex, run.Text)
			}
			if !run.Style.StrikeThrough || run.Style.Color != colorDone {
				t.Errorf("row %d run style = %+v, want completed styling", rowIndex, run.Style)
			}
		}
	}

	objective := groups[0].Rows[1].Runs[1]
	wantOpacity := 0.8 * 0.6 * 0.78
	if math.Abs(objective.Style.Opacity-wantOpacity) > 0.0001 {
		t.Errorf("objective opacity = %v, want nested global/done/objective %v",
			objective.Style.Opacity, wantOpacity)
	}
}

func TestOnslaughtRowsCarryColumnAndTightShadowCommands(t *testing.T) {
	cfg := config.Default()
	cfg.Widget.Block.Enabled = false
	cfg.Widget.Session.Enabled = false
	cfg.Widget.XP.Enabled = false
	cfg.Widget.Map.Enabled = false
	cfg.Widget.Challenges.Enabled = false

	now := time.Unix(30_000, 0)
	view := &model.View{
		Now:                   now,
		HaveData:              true,
		HasPosition:           true,
		PositionX:             3000,
		PositionY:             3000,
		HasOnslaughtCountdown: true,
		OnslaughtCountdown:    90 * time.Second,
		BlockEvents: []model.CityEvent{{
			Kind: model.EventSpawn, Enemies: []string{"Wraith"}, Start: now, End: now.Add(5 * time.Minute),
		}},
	}

	groups := BuildTextGroups(view, cfg, nil)
	if len(groups) != 1 || groups[0].Name != "bosses" {
		t.Fatalf("groups = %q, want bosses", groupNames(groups))
	}
	rows := groups[0].Rows
	if len(rows) < 2 || !rows[0].Distribute || len(rows[0].Runs) != 2 {
		t.Fatalf("onslaught header = %+v, want distributed title and timer", rows[0])
	}
	if got := rows[1].Runs[0]; got.MinWidthChars != 5 || got.GapAfter != 6 {
		t.Errorf("onslaught label column = %+v, want five characters plus six-pixel gap", got)
	}
	for _, run := range rows[1].Runs {
		if len(run.Style.Shadows) != 4 {
			t.Errorf("onslaught run has %d shadows, want tight four-corner outline",
				len(run.Style.Shadows))
		}
		for _, shadow := range run.Style.Shadows {
			if shadow.Blur != 0 {
				t.Errorf("tight shadow has blur %v", shadow.Blur)
			}
		}
	}
}

func TestBuildMapProducesCenteredVectorGeometryAndStyledKey(t *testing.T) {
	cfg := config.Default()
	cfg.HUD.MarginLeft = 20
	cfg.HUD.MarginRight = 30
	cfg.HUD.MarginTop = 10
	cfg.HUD.MarginBottom = 40
	cfg.HUD.Opacity = 0.5
	cfg.Widget.Map.Radius = 2
	cfg.Widget.Map.Opacity = 0.4
	cfg.Widget.Block.Enabled = false
	cfg.Widget.Bosses.Enabled = false
	cfg.Widget.Session.Enabled = false
	cfg.Widget.XP.Enabled = false
	cfg.Widget.Challenges.Enabled = false

	mark := model.CityMark{
		Marker: "M1", Label: "supply run", Kind: model.EventMission,
		X: 1001, Y: 1000, EndsIn: 5 * time.Minute,
	}
	view := &model.View{
		Now: time.Unix(40_000, 0), HaveData: true, HasPosition: true,
		PositionX: 1000, PositionY: 1000, CityMarks: []model.CityMark{mark},
	}
	viewport := Viewport{Width: 1920, Height: 1080}

	got := Build(view, cfg, viewport, nil)
	if got.Map == nil {
		t.Fatal("visible map produced no vector command group")
	}
	if len(got.TextGroups) != 0 {
		t.Fatalf("text groups = %q, want none", groupNames(got.TextGroups))
	}
	m := got.Map
	transform := newLayoutTransform(cfg, viewport)
	usableWidth := float64(cfg.HUD.ReferenceWidth-cfg.HUD.MarginLeft-cfg.HUD.MarginRight) *
		transform.scale
	usableHeight := float64(cfg.HUD.ReferenceHeight-cfg.HUD.MarginTop-cfg.HUD.MarginBottom) *
		transform.scale
	wantX := transform.offsetX + float64(cfg.HUD.MarginLeft)*transform.scale +
		(usableWidth-m.Width)/2 + float64(cfg.Widget.Map.OffsetX)*transform.scale
	wantY := transform.offsetY + float64(cfg.HUD.MarginTop)*transform.scale +
		(usableHeight-m.Height)/2 + float64(cfg.Widget.Map.OffsetY)*transform.scale
	if m.Position != (Point{X: wantX, Y: wantY}) {
		t.Errorf("centered map position = %+v, want %v,%v", m.Position, wantX, wantY)
	}
	if len(m.Cells) == 0 {
		t.Fatal("map contains no cell commands")
	}
	first := m.Cells[0]
	shade, ok := render.City().Shade(first.BlockX, first.BlockY)
	if !ok {
		t.Fatalf("scene emitted non-city cell %d,%d", first.BlockX, first.BlockY)
	}
	if want := shade.Alpha * 0.4 * 0.5; math.Abs(first.Alpha-want) > 0.0001 {
		t.Errorf("cell alpha = %v, want shade × map × HUD opacity %v", first.Alpha, want)
	}

	if len(m.Rings) != 8 {
		t.Errorf("ring segments = %d, want four event and four player segments", len(m.Rings))
	}
	var eventMarker *MapMarker
	for i := range m.Markers {
		if m.Markers[i].Text == "M1" {
			eventMarker = &m.Markers[i]
			break
		}
	}
	if eventMarker == nil {
		t.Fatal("event marker was not emitted")
	}
	if eventMarker.Opacity != cfg.HUD.Opacity {
		t.Errorf("event marker opacity = %v, map opacity must not dim marker", eventMarker.Opacity)
	}

	if len(m.ListRows) == 0 || len(m.ListRows[0].Runs) < 2 {
		t.Fatalf("map key rows = %+v, want explicit marker/timer/text runs", m.ListRows)
	}
	chip := m.ListRows[0].Runs[0]
	if chip.Text != "M1" || chip.Style.Background != render.CityMarkInk(mark).Hex() ||
		chip.Style.Color != mapMarkerInk {
		t.Errorf("map key chip = %+v", chip)
	}
	if len(chip.Style.Shadows) != 0 {
		t.Errorf("map key chip inherited %d shadows; dark text on a bright chip needs none",
			len(chip.Style.Shadows))
	}
}

func TestBuildMapHonorsVisibilityAndData(t *testing.T) {
	cfg := config.Default()
	view := &model.View{HaveData: true, HasPosition: true, PositionX: 1000, PositionY: 1000}

	if got := Build(view, cfg, Viewport{}, func(name string) bool { return name == "map" }); got.Map != nil {
		t.Error("hidden map still produced commands")
	}
	if got := Build(&model.View{}, cfg, Viewport{}, nil); got.Map != nil {
		t.Error("map without data, position, or marks still produced commands")
	}
}

func groupNames(groups []Group) string {
	names := make([]string, len(groups))
	for i, group := range groups {
		names[i] = group.Name
	}
	return strings.Join(names, ",")
}
