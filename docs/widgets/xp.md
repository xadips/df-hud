# XP/hr

`[widget.xp]`. Experience per hour from the last time cumulative XP moved.

![XP/hr with all four daily and weekly rows on](../images/xp.png)

A poll that repeats the same total is ignored. When the total changes, the
rate is that gain over the time since the previous change, scaled to an hour.
The rate stays blank until `min_samples` changes have landed (default 3).
After 300 seconds with no change, the next gain starts the count again.

Amber means a recent poll missed. Red means several have. Those colours win
over `color`. After a challenge reward dumps a lump into the last interval,
**U** (`hotkeys.xp_reset`) or the tray item **Reset xp/hr** starts it again.

`show_progress = true` prefixes the line with banked overflow past the current
level (`300%  Xp/Hr: …`) once `df_exp` is over the catalog threshold. At or
under 100% the prefix is omitted; the game sidebar already shows that. It
updates on each player-record poll (`poll.active_interval`, default 10
seconds). At the level cap, or if the catalog has not loaded, the prefix is
omitted.

`rows` adds record counters under the rate, in `color`, from the same poll,
top to bottom in the order listed. All six:

```toml
rows = ["daily_ts", "weekly_ts", "daily_loots", "weekly_loots", "daily_pks", "weekly_pks"]
```

```
Xp/Hr: 52,340,112
Daily TS: 41,201,544
Weekly TS: 2,364,345,710
Daily Loots: 3
Weekly Loots: 10
Daily PKs: 0
Weekly PKs: 3
```

TS is `df_expdeath_daily` / `df_expdeath_weekly`: XP since your last death
within the day or week. Loots are `df_loots_daily` / `df_loots_weekly`, PKs
(player kills) `df_playerkills_daily` / `df_playerkills_weekly`. A field
missing from the record shows `--`. Four rows fit above the default
challenges spot; with more, raise `[widget.challenges] y`.

| Key | Default | |
| --- | --- | --- |
| `enabled` | `true` | On or off |
| `x`, `y` | `220`, `80` | Position at 2560x1440 |
| `prefix` | `"Xp/Hr: "` | Text before the number |
| `show_progress` | `false` | Overflow `%` to the left of the prefix |
| `rows` | `[]` | Counters under the rate: `daily_ts`, `weekly_ts`, `daily_loots`, `weekly_loots`, `daily_pks`, `weekly_pks` |
| `color` | `#ffffff` | Normal colour. Amber and red still win. |
| `window` | `60` | Unused for the rate; kept so existing configs still parse |
| `min_samples` | `3` | Changes before a rate is shown |
