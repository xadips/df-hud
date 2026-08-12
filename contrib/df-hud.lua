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
-- Commented out because it fires on every left click, which is also how you shoot.
-- Each one costs a curl while no run is in progress, and nothing once one is:
-- df-hud answers immediately without asking the compositor anything.
--
-- hl.bind("mouse:272", post("/api/run/click"),
--     { non_consuming = true, click = true, description = "df-hud: catch the Start button" })

-- The overlay is text over a game, redrawn every second. It never wants animating,
-- blurring, or its transparent parts treated as something to dim behind.
hl.layer_rule({
    name    = "df-hud",
    match   = { namespace = "^(df-hud)$" },
    no_anim = true,
    blur    = false,
})
