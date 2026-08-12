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

local function post(path)
    -- --max-time so a wedged listener cannot leave a curl per keypress lying
    -- around, and -o /dev/null so nothing is written anywhere.
    return hl.dsp.exec_cmd("curl -fsS --max-time 2 -o /dev/null -X POST " .. endpoint .. path)
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
-- The bind is ENABLED ONLY WHILE THE GAME IS FOCUSED, which is the difference
-- between this being usable and being a nuisance: a plain global bind on the left
-- mouse button spawns a curl on every click you make all day, in every
-- application. Hyprland has no per-window bind filter, so it is done with an event
-- subscription and set_enabled - the bind simply does not exist while you are
-- anywhere else.
--
-- Set catch_start_button to true to use it. Even then it fires on every click
-- inside the game, which is also how you shoot: each costs one curl while no run
-- is in progress, and nothing at all once one is, because df-hud answers those
-- immediately without asking the compositor anything.
local catch_start_button = false

if catch_start_button then
    local catch = hl.bind("mouse:272", post("/api/run/click"), {
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
