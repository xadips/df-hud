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
--
-- The fallback exists because an instance may be listening inside a network
-- namespace. Loopback is per namespace, so a df-hud running in one has a
-- 127.0.0.1 that this curl -- issued by the compositor, in the init namespace --
-- cannot reach at all, and every keybind silently did nothing. Trying the plain
-- endpoint first keeps the normal case a single connect; when nothing answers
-- there, the same request is retried inside the namespace. A refused connection
-- is immediate, so the extra attempt costs nothing perceptible.
--
-- Ordered this way round on purpose: the un-namespaced instance is the common one,
-- and df-netns-run exits non-zero when the namespace is down, so neither branch
-- can hang.
local function post_cmd(path)
    local req = "curl -fsS --max-time 2 -o /dev/null -X POST " .. endpoint .. path
    return req .. " || df-netns-run " .. req
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

-- CHOOSING KEYS. The game's own bindings are readable, and worth reading before you
-- claim a key is free - knowledge/game-keybinds.md has the recipe. They live in the
-- Wine registry as Unity PlayerPrefs, under
-- HKCU\Software\Creaky Corpse\Dead Frontier in the Proton prefix's user.reg.
--
-- Measured there, the game holds: e f (action), ` and \ (CHAT), [ ] = (weapon cycle
-- and minimap), mouse 0 and u (fire), z (fire toggle), space and left shift (run),
-- return escape q (ui), 1 (weapon). The map was briefly on a bare grave here with a
-- comment saying the game did nothing with it - grave is the chat key.
--
-- That list is only what the launcher EXPOSES. M opens the map in game and is not in
-- it, so some keys are hardcoded: reading it rules a collision in, never out. Keep off
-- the function keys and the number row too.
--
-- The key matters even under a modifier: a consuming compositor bind should stop the
-- game ever seeing the keypress, but a client that polls raw key state does not care
-- what else is held down.

-- Start the run clock from now.
--
-- Worth having on a key even with the click detector below: nothing in the player
-- record marks the client taking control, so if the clock ever starts late, this
-- is the correction.
bind_action("grave", "/api/run/start", "df-hud: restart run clock")

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
bind_action("t", "/api/widget/challenges/toggle",
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

