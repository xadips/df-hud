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
-- hyprland.conf syntax, for anyone not using the Lua config.
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

-- SUPER + ALT is used because the game itself uses the function keys, and because
-- everything under it is free here except SUPER + ALT + R.

-- Start the run clock from now.
--
-- Worth having on a key even with the click detector below: nothing in the player
-- record marks the client taking control, so if the clock ever starts late, this
-- is the correction.
hl.bind("SUPER + ALT + T", post("/api/run/start"), { description = "df-hud: restart run clock" })

-- Start the xp/hr average again from now. For after a challenge reward drops a
-- lump of XP into the window and it stops answering "how fast am I killing".
hl.bind("SUPER + ALT + X", post("/api/xp/reset"), { description = "df-hud: reset xp/hr" })

-- Show or hide the overlay by hand. The automatic rules still apply on top: this
-- cannot make the HUD appear over a game that is not running.
hl.bind("SUPER + ALT + O", post("/api/overlay/toggle"), { description = "df-hud: toggle overlay" })

-- The console window, once it exists.
hl.bind("SUPER + ALT + C", post("/api/console/toggle"), { description = "df-hud: console" })

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
--   1. The bind EXISTS ONLY WHILE THE GAME IS FOCUSED. Hyprland has no per-window
--      bind filter, so it is done with an event subscription and set_enabled - the
--      bind simply is not there while you are anywhere else.
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

    local catch = hl.bind("mouse:272", throttled, {
        non_consuming = true,
        click = true,
        description = "df-hud: catch the Start button",
    })

    -- Matched on the class rather than the exact name because Wine reports it
    -- lowercased, and on a substring so a rename to DeadFrontier2.exe or similar
    -- does not silently stop this working.
    local function is_game(window)
        return window ~= nil and type(window.class) == "string"
            and window.class:lower():find("deadfrontier", 1, true) ~= nil
    end

    local function arm()
        -- The event's argument shape is not something to rely on, so the focused
        -- window is asked for directly.
        catch:set_enabled(is_game(hl.get_active_window()))
    end

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
