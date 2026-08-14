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
-- all, which is what makes the bare keys below affordable at all: outside the game
-- they are still themselves in every terminal and editor you have.
--
-- Three things to know before leaving this on:
--
--   * the overlay toggle stops working when you alt-tab off the game, which is
--     sometimes exactly when you want it - the HUD is hidden by workspace, not by
--     focus, so a browser in front of the game on the same workspace still has the
--     overlay over it. Pass always = true to bind_action to exempt one key.
--   * a disabled bind is silent. Pressing it does nothing and says nothing, which
--     looks identical to df-hud being down. `hyprctl binds` still lists it.
--   * a bare key is CONSUMED while the game is focused, so the game never sees it -
--     including in its own chat and trade boxes. Every bare letter below is a letter
--     you cannot type in game. SHIFT plus the same key still gets through, since
--     Hyprland matches the modifier mask exactly.
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

-- CHOOSING KEYS. Keep off the function keys and the number row, which the game uses
-- itself, and off anything you type in its chat box - see the consuming note above.
--
-- The key matters even under a modifier. The map was on M, which is the game's own
-- map key: a consuming compositor bind should stop the game ever seeing the keypress,
-- but a client that polls raw key state does not care what else is held down, so bind
-- something the game does nothing with.

-- Start the run clock from now.
--
-- Worth having on a key even with the click detector below: nothing in the player
-- record marks the client taking control, so if the clock ever starts late, this
-- is the correction.
bind_action("t", "/api/run/start", "df-hud: restart run clock")

-- Start the xp/hr average again from now. For after a challenge reward drops a
-- lump of XP into the window and it stops answering "how fast am I killing".
bind_action("x", "/api/xp/reset", "df-hud: reset xp/hr")

-- Show or hide the overlay by hand. The automatic rules still apply on top: this
-- cannot make the HUD appear over a game that is not running.
bind_action("k", "/api/overlay/toggle", "df-hud: toggle overlay")

-- Hide the challenge board without turning it off in the config.
--
-- Per group rather than one key per widget: the endpoint takes the group's name, so
-- "block", "bosses", "session" and "xp" work the same way if you want keys for
-- them. The status banner deliberately cannot be hidden - it is how df-hud says it
-- cannot do its job.
bind_action("tab", "/api/widget/challenges/toggle",
    "df-hud: toggle the challenge board")

-- The city map: the whole 59x55 grid, shaded the way DFProfiler's own map shades
-- it, with an identifier on every active event and a ring on the block you are
-- standing on. It starts hidden and this is what brings it up - it is 826 pixels of
-- city, which is worth having for ten seconds and not worth having permanently.
--
-- Same endpoint as every other group, because it IS another group: it inherits the
-- HUD's click-through, so the mouse still reaches the game through it.
--
-- A BARE KEY, with no modifier, which is only reasonable because of the focus gate
-- above: outside the game this bind does not exist, so the key is still itself in
-- every terminal and editor you have. Do not copy a bare key into df-hud.hypr.conf -
-- a bind in that dialect is global, and it would eat that key everywhere.
--
-- Hyprland matches the modifier mask exactly, so SHIFT + the same key still reaches
-- the game. What a bare key does cost is that key inside the game: it is a consuming
-- bind, so the game never sees it - including in its own chat box.
bind_action("v", "/api/widget/map/toggle",
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
-- OFF BY DEFAULT, BECAUSE IT CRASHED THE COMPOSITOR.
--
-- 2026-08-14, Hyprland 0.56.2: a left click segfaulted Hyprland inside its own Lua
-- bindings. The backtrace is a mouse button arriving at CKeybindManager::handleKeybinds,
-- which pcalls this Lua function, which calls hl.exec_cmd, and the argument check for
-- that (Config::Lua::Bindings::Check::string) dies on a null dereference. The whole
-- session goes with it, mid-game.
--
-- This is a bug in Hyprland - a compositor must not segfault on a callback its own
-- config gave it - but the trigger is here, and it is the only bind in this file whose
-- dispatcher is a Lua FUNCTION rather than a dispatcher object built at load time.
-- Every key below is dispatched in C++ and none of them was in the backtrace.
--
-- Two details from that crash that are worth keeping:
--
--   * the game had closed five minutes earlier, so window.close should have disarmed
--     this bind. Either set_enabled does not take on a mouse bind in this version, or
--     what fired was a stale bind left over by an hyprctl reload. Both readings say
--     the same thing: do not put a Lua callback on the input path.
--   * two hyprctl reloads had happened that afternoon. If it is the stale-bind
--     reading, a reload is what arms the gun.
--
-- Turning this on gets you the feature and the risk. The alternative that keeps the
-- input path free of Lua is a plain exec dispatcher, which loses the rate limit and
-- so forks a curl per click while the game is focused - several a second in a fight:
--
--   table.insert(gated, hl.bind("mouse:272", post("/api/run/click"),
--       { non_consuming = true, click = true, description = "df-hud: catch the Start button" }))
--
-- Either way SUPER + T starts the clock by hand, which is what this was saving you.
local catch_start_button = false

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
-- The LAUNCHER is not the game, and only the title says so. Its dialogs are the same
-- executable, so they report the same class:
--
--     class: deadfrontier.exe   title: Dead Frontier Configuration
--     class: deadfrontier.exe   title: Input Configuration
--
-- Arming a bare grave or tab while you are typing in a settings box would be a poor
-- trade. The title is checked defensively rather than assumed: if this Hyprland does
-- not report one, the class alone still decides, which is what this did before.
local function is_game(window)
    if window == nil or type(window.class) ~= "string" then
        return false
    end
    if window.class:lower():find("deadfrontier", 1, true) == nil then
        return false
    end
    if type(window.title) == "string" and window.title:lower():find("configuration", 1, true) then
        return false
    end
    return true
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

    -- Subscribe FIRST, then arm once.
    --
    -- That order is deliberate, and so is the pcall. This runs during config load,
    -- and on a fresh compositor start there may be no window to ask about yet - an
    -- error out of hl.get_active_window would abort the rest of this file, taking the
    -- subscriptions and the layer rule below with it and leaving every bind in
    -- whatever state it was created in. Subscribed first, the worst case is that the
    -- binds are wrong until you next change focus, which fixes itself.
    --
    -- Not hypothetical: on the boot of 2026-08-14 the mouse bind below never
    -- appeared, while all five keys did, which is what a truncated load looks like.
    hl.on("window.active", arm)
    -- Closing the game does not always produce a window.active, so the close is
    -- watched too: a bind left armed with no game focused is the exact thing this
    -- is here to avoid.
    hl.on("window.close", arm)
    -- And once now, or a reload while you are reading this would leave every bind
    -- armed until the next time focus moved.
    pcall(arm)
end

-- The overlay is text over a game, redrawn every second. It never wants animating,
-- blurring, or its transparent parts treated as something to dim behind.
hl.layer_rule({
    name    = "df-hud",
    match   = { namespace = "^(df-hud)$" },
    no_anim = true,
    blur    = false,
})

