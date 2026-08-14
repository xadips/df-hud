-- df-hud keybinds and layer rules, for Hyprland's Lua configuration.
--
-- Add one line to conf.d/binds.lua (or wherever you keep binds):
--
--     require("df-hud")
--
-- after putting this file on package.path - the simplest way is a symlink:
--
--     ln -s ~/Programming/df-hud/contrib/df-hud.lua ~/.config/hypr/conf.d/df-hud.lua
--
-- There is a df-hud.hypr.conf next to this file with the same binds in the plain
-- hyprland.conf syntax, for anyone not using the Lua config. It cannot do the
-- focus gating below - that dialect has no event subscriptions and no way to
-- enable a bind conditionally - which is the main reason to prefer this file.
--
-- WHY THE KEYS LIVE HERE AND NOT IN df-hud's OWN CONFIG
--
-- A Wayland client cannot grab a global hotkey. That is deliberate in the
-- protocol, not an oversight: the compositor owns input routing, and nothing else
-- gets to see keys pressed at other windows. So df-hud publishes each action on
-- its loopback listener and the binding is yours - which also means you can change
-- a key without restarting df-hud.

local endpoint = "http://127.0.0.1:9275"

-- --max-time so a wedged listener cannot leave a curl per keypress lying around,
-- and -o /dev/null so nothing is written anywhere.
local function post_cmd(path)
    return "curl -fsS --max-time 2 -o /dev/null -X POST " .. endpoint .. path
end

-- Two forms of the same call, because Hyprland has two: hl.dsp.exec_cmd builds a
-- dispatcher for a bind, hl.exec_cmd runs a command now and is what a Lua
-- dispatcher function has to use.
local function post(path)
    return hl.dsp.exec_cmd(post_cmd(path))
end

-- THE KEYS EXIST ONLY WHILE DEAD FRONTIER IS FOCUSED.
--
-- Hyprland has no per-window bind filter, so this is done the same way the click
-- catcher at the bottom of this file does it: the binds are created, then enabled
-- and disabled as focus moves. While you are anywhere else they are not there at
-- all, so SUPER + ALT + D is free for whatever your other windows want it for.
--
-- Two things to know before leaving this on:
--
--   * SUPER + ALT + K stops working when you alt-tab off the game, which is
--     sometimes exactly when you want it - the HUD is hidden by workspace, not by
--     focus, so a browser in front of the game on the same workspace still has the
--     overlay over it. Pass always = true to bind_action to exempt one key.
--   * a disabled bind is silent. Pressing it does nothing and says nothing, which
--     looks identical to df-hud being down. `hyprctl binds` still lists it.
--
-- Set this to false to have the keys work everywhere. The click catcher is gated
-- either way - a global bind on the left mouse button would fork a curl on every
-- click you make all day.
local only_when_game_focused = true

-- Binds that follow the game's focus. Handles rather than names, because
-- set_enabled is a method on what hl.bind returns.
local gated = {}

-- bind_action wires one key to one endpoint of the loopback listener. always = true
-- keeps a bind armed everywhere even when only_when_game_focused is set.
local function bind_action(keys, path, description, always)
    local kb = hl.bind(keys, post(path), { description = description })
    if only_when_game_focused and not always then
        table.insert(gated, kb)
    end
    return kb
end

-- SUPER + ALT is used because the game itself uses the function keys, and because
-- everything under it is free here except SUPER + ALT + R.
--
-- The LETTER still matters even under two modifiers. The map was on M, which is the
-- game's own map key: a consuming compositor bind should stop the game ever seeing
-- the keypress, but a client that polls raw key state does not care what else is
-- held down, so the safe choice is a letter the game does nothing with. Hence D.

-- Start the run clock from now.
--
-- Worth having on a key even with the click detector below: nothing in the player
-- record marks the client taking control, so if the clock ever starts late, this
-- is the correction.
bind_action("SUPER + T", "/api/run/start", "df-hud: restart run clock")

-- Start the xp/hr average again from now. For after a challenge reward drops a
-- lump of XP into the window and it stops answering "how fast am I killing".
bind_action("SUPER + X", "/api/xp/reset", "df-hud: reset xp/hr")

-- Show or hide the overlay by hand. The automatic rules still apply on top: this
-- cannot make the HUD appear over a game that is not running.
bind_action("SUPER  + K", "/api/overlay/toggle", "df-hud: toggle overlay")

-- Hide the challenge board without turning it off in the config. B for board.
--
-- Per group rather than one key per widget: the endpoint takes the group's name, so
-- "block", "bosses", "session" and "xp" work the same way if you want keys for
-- them. The status banner deliberately cannot be hidden - it is how df-hud says it
-- cannot do its job.
bind_action("SUPER + B", "/api/widget/challenges/toggle",
    "df-hud: toggle the challenge board")

-- The city map: the whole 59x55 grid, shaded the way DFProfiler's own map shades
-- it, with an identifier on every active event and a ring on the block you are
-- standing on. It starts hidden and this is what brings it up - it is 826 pixels of
-- city, which is worth having for ten seconds and not worth having permanently.
--
-- Same endpoint as every other group, because it IS another group: it inherits the
-- HUD's click-through, so the mouse still reaches the game through it.
bind_action("SHIFT + D", "/api/widget/map/toggle",
    "df-hud: toggle the city map")

-- No bind for /api/console/toggle. The console does not exist yet - the endpoint
-- answers 503 - and SUPER + C is the window kill in this config, so a neighbouring
-- SUPER + ALT + C is a keystroke away from closing something. Bind it when there is
-- something to open.

-- OPTIONAL: start the run clock from the game's own Start button.
--
-- non_consuming is the whole trick, and it is why this cannot be done from inside
-- df-hud: a Wayland surface that receives a pointer event CONSUMES it, and there is
-- no forwarding. Only the compositor can both act on a click and still deliver it
-- to the game, because it is the thing doing the delivering.
--
-- df-hud never sees your input. It is told that a click happened and then checks
-- for itself whether the cursor was on the button, inside the game's focused
-- window, with no run already in progress. Configure the rectangle under
-- [run_start] in df-hud's config; the default is measured at 2560x1440.
--
-- Two things keep this from being a nuisance, because clicking is also how you
-- shoot and a plain global bind on the left mouse button would fork a curl several
-- times a second all day, in every application:
--
--   1. The bind EXISTS ONLY WHILE THE GAME IS FOCUSED - the same gate the keys
--      above use, and the reason it is written once, below. Unlike them this one is
--      not optional: a global click bind is a curl per shot fired.
--   2. The dispatcher is a FUNCTION rather than exec_cmd, so it runs inside the
--      compositor and decides whether to spawn anything, and at most one report
--      per second leaves the compositor.
--
-- One per second loses nothing. The Start press is a single click, and any click
-- dropped by that limit was under a second behind one df-hud has already judged -
-- so the only way to miss the button is to press it twice in one second, by which
-- point the clock is already running.
--
-- Set catch_start_button to false to do without it and start the clock with
-- SUPER + ALT + T instead.
local catch_start_button = true

if catch_start_button then
    local report = post_cmd("/api/run/click")

    -- os.time has one-second granularity, which is exactly the resolution wanted
    -- here. It is checked for existence rather than assumed: an embedded Lua
    -- without the os library should lose the rate limit, not the whole feature.
    local last = 0
    local function throttled()
        if os and os.time then
            local now = os.time()
            if now == last then
                return
            end
            last = now
        end
        hl.exec_cmd(report)
    end

    table.insert(gated, hl.bind("mouse:272", throttled, {
        non_consuming = true,
        click = true,
        description = "df-hud: catch the Start button",
    }))
end

-- Arming, for every gated bind at once.
--
-- Matched on the class rather than the exact name because Wine reports it
-- lowercased, and on a substring so a rename to DeadFrontier2.exe or similar does
-- not silently stop this working.
local function is_game(window)
    return window ~= nil and type(window.class) == "string"
        and window.class:lower():find("deadfrontier", 1, true) ~= nil
end

if #gated > 0 then
    local function arm()
        -- The event's argument shape is not something to rely on, so the focused
        -- window is asked for directly.
        local on = is_game(hl.get_active_window())
        for _, kb in ipairs(gated) do
            kb:set_enabled(on)
        end
    end

    -- Once at load, or a reload while you are reading this would leave every bind
    -- armed until the next time focus moved.
    arm()
    hl.on("window.active", arm)
    -- Closing the game does not always produce a window.active, so the close is
    -- watched too: a bind left armed with no game focused is the exact thing this
    -- is here to avoid.
    hl.on("window.close", arm)
end

-- The overlay is text over a game, redrawn every second. It never wants animating,
-- blurring, or its transparent parts treated as something to dim behind.
hl.layer_rule({
    name    = "df-hud",
    match   = { namespace = "^(df-hud)$" },
    no_anim = true,
    blur    = false,
})
