package main

import (
	"context"
	"df-hud/internal/config"
	"df-hud/internal/desktop"
	"df-hud/internal/game"
	"df-hud/internal/model"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

func reportConfig(cfg *config.Config, path string) {
	fmt.Printf("config ok (%s)\n", describeConfigSource(cfg, path))
	fmt.Printf("request budget: about %.0f/hour while playing, %.0f/hour idle\n",
		cfg.RequestsPerHour(1), cfg.RequestsPerHour(0))
}

func describeConfigSource(cfg *config.Config, path string) string {
	if cfg.SourcePath() == "" {
		return "built-in defaults, no config file at " + path
	}
	return "config " + cfg.SourcePath()
}

func reportGameDetection(cfg *config.Config) {
	fmt.Println(game.ScanDescription(cfg.Game.Process))
	state, err := game.Scan(cfg.Game.Process)
	if err != nil {
		fmt.Println(game.ScanError(err))
		return
	}
	if state.Running {
		fmt.Printf("FOUND: pid %d, launched %s (%s ago)\n",
			state.PID, state.StartedAt.Format(time.DateTime),
			state.Elapsed(time.Now()).Round(time.Second))
		reportWindowDetection(cfg, state)
		return
	}

	fmt.Println("NOT FOUND - the game does not appear to be running.")
	if candidates := game.SimilarProcesses("frontier"); len(candidates) > 0 {
		fmt.Println("\nProcesses with a similar name, in case the executable is named differently:")
		for _, candidate := range candidates {
			fmt.Println("  " + candidate)
		}
		fmt.Println("\nIf one of those is the game, set game.process to its basename.")
	} else {
		fmt.Println("Nothing similar is running either. Start the game and run this again.")
	}
	if cfg.Poll.OnlyWhenGameRunning {
		fmt.Println("\nNote: poll.only_when_game_running is on, so df-hud will not poll at all " +
			"until the game is detected.")
	}
}

func reportWindowDetection(cfg *config.Config, state model.GameState) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := desktop.NewClient()
	match := cfg.Game.WindowMatch()
	place, err := client.GameWindow(ctx, state.PID, desktop.Match{
		Class:         match.Class,
		IgnoreTitles:  match.IgnoreTitles,
		LauncherTitle: match.LauncherTitle,
	})
	if err != nil {
		fmt.Printf("\nThe desktop could not be asked where the window is (%v).\n"+
			"The HUD will still work; window-following visibility is disabled.\n", err)
		return
	}
	if place.Known {
		shown := "no - the HUD will be hidden while it stays there"
		if place.OnActiveWorkspace {
			shown = "yes"
		}
		if place.ForegroundRule {
			fmt.Printf("\nWINDOW: class %q on monitor %s (matched by %s)\n",
				place.Class, place.Monitor, place.MatchedBy)
			fmt.Printf("        that window is in the foreground: %s\n", shown)
		} else {
			fmt.Printf("\nWINDOW: class %q on monitor %s, workspace %s (matched by %s)\n",
				place.Class, place.Monitor, place.WorkspaceName, place.MatchedBy)
			fmt.Printf("        that workspace is being shown: %s\n", shown)
		}
		return
	}

	fmt.Printf("\nWINDOW NOT MATCHED: no window with pid %d, and none whose class looks like %q.\n",
		state.PID, match.Class)
	if ignore := cfg.Game.WindowTitleIgnore; len(ignore) > 0 {
		fmt.Printf("        (a title containing %q is skipped as the launcher: game.window_title_ignore)\n",
			strings.Join(ignore, ", "))
	}
	if windows, err := client.Windows(ctx); err == nil {
		fmt.Println("\nTop-level windows the desktop knows about:")
		for _, window := range windows {
			title := window.Title
			if len(title) > 40 {
				title = title[:40] + "..."
			}
			fmt.Printf("  class %-32s pid %-8d %s\n", window.Class, window.PID, title)
		}
		fmt.Println("\nIf one of those is the game, set game.window_class to its class.")
	}
	fmt.Println("\nUntil then the HUD cannot follow the game window, and monitor = \"auto\" " +
		"leaves placement to the desktop.")
}

var withheldFieldPattern = regexp.MustCompile(`(?i)pass|token|cookie|auth|secretkey|^sc$|session`)

func dumpRecordFields(vars map[string]string) {
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("%d fields returned:\n", len(names))
	for _, name := range names {
		value := vars[name]
		if withheldFieldPattern.MatchString(name) {
			value = "[withheld]"
		}
		fmt.Printf("  %s = %s\n", name, value)
	}
}

func printViewJSON(view *model.View) {
	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		log.Printf("view: %v", err)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}
