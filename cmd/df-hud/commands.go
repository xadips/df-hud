package main

import (
	"context"
	"df-hud/internal/challenges"
	hudformat "df-hud/internal/format"
	"fmt"
	"log"
	"time"
)

// runOnce polls once, prints the derived view, and exits.
func (a *app) runOnce(ctx context.Context, dumpFields bool) {
	tick := a.poller.Once(ctx)
	if tick.Err != nil {
		log.Fatalf("poll failed: %v", tick.Err)
	}
	if dumpFields {
		dumpRecordFields(tick.Vars)
	}
	a.store.ApplyTick(tick)
	a.store.SetGame(a.game.State())
	a.store.SetPollerStatus(a.poller.Status())

	if state, err := a.game.Scan(); err == nil {
		a.store.SetGame(state)
	}
	printViewJSON(a.store.Derive(time.Now()))
}

// dumpChallenges fetches the signed challenge board once and prints it.
func (a *app) dumpChallenges(ctx context.Context, raw bool) {
	cr, _, ok := a.creds.Get()
	if !ok {
		log.Fatal("no credentials yet: load a Dead Frontier page with the bridge userscript")
	}
	a.client.Cookie = cr.Cookie
	salt := a.Config().SigningSalt(a.creds.Salt)
	if salt == "" {
		log.Fatal("no signing salt: the bridge has not reported one, and df.skeygen is empty. " +
			"Load the Outpost home page with the bridge userscript or the the bridge userscript installed.")
	}

	reqCtx, cancel := context.WithTimeout(ctx, a.Config().DF.Timeout.Duration)
	defer cancel()
	vars, err := a.client.LoadChallenge(reqCtx, cr, salt)
	if err != nil {
		log.Fatalf("load_challenge failed: %v", err)
	}
	if raw {
		dumpRecordFields(vars)
		return
	}

	if _, ok := a.store.Snapshot(); !ok {
		if tick := a.poller.Once(ctx); tick.Err == nil {
			a.store.ApplyTick(tick)
		} else {
			log.Printf("could not read your level (%v); reward XP will be omitted", tick.Err)
		}
	}
	level, gold := 0, false
	if snap, ok := a.store.Snapshot(); ok {
		level, gold = snap.Level, snap.GoldMember
	}
	board := challenges.Parse(vars, level, gold)
	fmt.Printf("%d challenges (level %d)\n", len(board), level)
	for _, challenge := range board {
		kind := "personal"
		if challenge.Clan {
			kind = "clan"
		}
		status := " "
		if challenge.Complete() {
			status = "x"
		}
		fmt.Printf("\n[%s] %-8s %s\n", status, kind, challenge.Name)
		if remaining := challenge.Remaining(time.Now()); remaining > 0 {
			fmt.Printf("        ends in %s\n", hudformat.Countdown(remaining))
		}
		for _, objective := range challenge.Objectives {
			fmt.Printf("        %-28s %s / %s  (%.0f%%)\n",
				objective.Name, hudformat.Int(objective.Score), hudformat.Int(objective.Target),
				objective.Fraction()*100)
		}
		switch {
		case challenge.RewardPoints > 0:
			fmt.Printf("        reward: %d clan points\n", challenge.RewardPoints)
		case challenge.RewardExp > 0:
			fmt.Printf("        reward: %s xp\n", hudformat.Int(challenge.RewardExp))
		}
		if challenge.RewardSpecial != "" {
			fmt.Printf("        reward: %s\n", challenge.RewardSpecial)
		}
	}
}
