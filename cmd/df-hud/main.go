package main

import (
	"context"
	"df-hud/internal/config"
	"flag"
	"fmt"
	"log"
	"os/signal"
)

// version is stamped by the Makefile and Windows packaging script.
var version = "0.1.0-dev"

type options struct {
	configPath     string
	once           bool
	printView      bool
	showVersion    bool
	checkConfig    bool
	checkGame      bool
	dumpFields     bool
	dumpChallenges bool
	headless       bool
	printHUD       bool
}

func parseOptions() options {
	var opts options
	flag.StringVar(&opts.configPath, "config", config.DefaultPath(), "path to config.toml")
	flag.BoolVar(&opts.once, "once", false, "poll once, print the view, and exit")
	flag.BoolVar(&opts.printView, "print-view", false, "print the derived view as JSON on every update")
	flag.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	flag.BoolVar(&opts.checkConfig, "check-config", false, "validate the config and exit")
	flag.BoolVar(&opts.checkGame, "check-game", false, "report whether the game client is detected, and exit")
	flag.BoolVar(&opts.dumpFields, "dump-fields", false, "with -once, print the player record for diagnostics (credentials withheld)")
	flag.BoolVar(&opts.dumpChallenges, "dump-challenges", false, "fetch the challenge board once, print it, and exit")
	flag.BoolVar(&opts.headless, "headless", false, "run without the HUD window")
	flag.BoolVar(&opts.printHUD, "print-hud", false, "print the HUD's text lines on every update")
	flag.Parse()
	return opts
}

func main() {
	log.SetFlags(log.Ltime)
	opts := parseOptions()
	if opts.showVersion {
		fmt.Printf("df-hud %s\n", version)
		return
	}

	config.Version = version
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if opts.checkConfig {
		reportConfig(cfg, opts.configPath)
		return
	}
	if opts.checkGame {
		reportGameDetection(cfg)
		return
	}
	log.Printf("df-hud %s starting (%s)", version, describeConfigSource(cfg, opts.configPath))

	if err := prepareConfig(cfg); err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	oneShot := opts.once || opts.dumpChallenges
	app, err := newApp(ctx, cfg, opts.configPath, !oneShot)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer app.Close()

	switch {
	case opts.dumpChallenges:
		app.dumpChallenges(ctx, opts.dumpFields)
	case opts.once:
		app.runOnce(ctx, opts.dumpFields)
	default:
		app.run(ctx, runOptions{
			printView: opts.printView,
			printHUD:  opts.printHUD,
			hud:       cfg.HUD.Enabled && !opts.headless,
			quit:      stop,
		})
	}
}
