-- Smoke test for df-hud.lua, run by TestHyprlandLuaSnippet in lua_test.go.
--
-- It exists because that file is the one piece of df-hud that Go cannot reach: it
-- runs inside the compositor, its API is Hyprland's, and the only way to find out
-- that a method was misnamed is normally to reload the config and read the log. So
-- `hl` is stubbed here and the snippet is loaded against it, which pins down the
-- three things that are easy to get silently wrong: the bind is created DISABLED,
-- focusing the game arms it, and the click reporter is rate limited.
--
-- What this cannot check is whether Hyprland's real API behaves as the stub does.
-- Only a reload can tell you that.

local calls = { binds = {}, layer_rules = {}, execs = {}, handlers = {} }

local active_window = { class = "kitty" }

local function fake_keybind()
    local kb = { enabled = true }
    function kb:set_enabled(on)
        self.enabled = on
    end
    return kb
end

hl = {
    dsp = {
        exec_cmd = function(cmd)
            return { kind = "exec_cmd", cmd = cmd }
        end,
    },
    exec_cmd = function(cmd)
        table.insert(calls.execs, cmd)
    end,
    bind = function(keys, dispatcher, opts)
        local kb = fake_keybind()
        table.insert(calls.binds, {
            keys = keys,
            dispatcher = dispatcher,
            opts = opts or {},
            keybind = kb,
        })
        return kb
    end,
    layer_rule = function(rule)
        table.insert(calls.layer_rules, rule)
    end,
    on = function(event, fn)
        calls.handlers[event] = fn
    end,
    get_active_window = function()
        return active_window
    end,
}

-- Time is driven by hand so the rate limit can be tested without sleeping.
local now = 1000
os.time = function()
    return now
end

local snippet = assert(arg[1], "usage: df-hud_spec.lua <path to df-hud.lua>")
assert(loadfile(snippet))()

local failures = 0
local function check(ok, what)
    if not ok then
        failures = failures + 1
        io.stderr:write("FAIL: " .. what .. "\n")
    end
end

local function find_bind(keys)
    for _, b in ipairs(calls.binds) do
        if b.keys == keys then
            return b
        end
    end
end

-- The keybinds, and that each posts to the endpoint it claims to.
local want = {
    ["SUPER + ALT + T"] = "/api/run/start",
    ["SUPER + ALT + X"] = "/api/xp/reset",
    ["SUPER + ALT + K"] = "/api/overlay/toggle",
    ["SUPER + ALT + B"] = "/api/widget/challenges/toggle",
    ["SUPER + ALT + D"] = "/api/widget/map/toggle",
}
for keys, path in pairs(want) do
    local b = find_bind(keys)
    check(b ~= nil, keys .. " is bound")
    if b then
        check(type(b.dispatcher) == "table" and b.dispatcher.kind == "exec_cmd",
            keys .. " uses an exec_cmd dispatcher")
        check(b.dispatcher.cmd:find(path, 1, true) ~= nil, keys .. " posts to " .. path)
        check(b.dispatcher.cmd:find("127.0.0.1", 1, true) ~= nil, keys .. " stays on loopback")
    end
end

-- The layer rule, which is what keeps the overlay from being animated or blurred.
check(#calls.layer_rules == 1, "one layer rule is registered")
if calls.layer_rules[1] then
    local rule = calls.layer_rules[1]
    check(rule.match ~= nil and rule.match.namespace ~= nil, "the layer rule matches a namespace")
    check(rule.no_anim == true, "the layer rule disables animation")
end

-- The click catcher. non_consuming is the whole point: without it the click that
-- starts the run never reaches the game, so pressing Start would do nothing.
local catch = find_bind("mouse:272")
check(catch ~= nil, "the click catcher is bound")
if catch then
    check(catch.opts.non_consuming == true, "the click is passed through to the game")
    check(catch.opts.click == true, "the bind is a click bind")
    check(type(catch.dispatcher) == "function", "the dispatcher is a Lua function, so it can filter")

    -- Armed only while the game is focused. kitty was focused at load, so the
    -- bind must have been switched off on its way out.
    check(catch.keybind.enabled == false, "the bind is disabled while another window is focused")

    local arm = calls.handlers["window.active"]
    check(arm ~= nil, "window.active is subscribed")
    check(calls.handlers["window.close"] ~= nil, "window.close is subscribed")

    if arm then
        -- Wine reports the class lowercased, which is why the match is not exact.
        active_window = { class = "deadfrontier.exe" }
        arm()
        check(catch.keybind.enabled == true, "focusing the game arms the bind")

        active_window = nil -- nothing focused at all, e.g. the game just closed
        arm()
        check(catch.keybind.enabled == false, "an empty desktop disarms the bind")

        active_window = { class = "deadfrontier.exe" }
        arm()
    end

    -- The rate limit: clicking is also how you shoot, so a fight must not fork a
    -- curl per shot.
    catch.dispatcher()
    check(#calls.execs == 1, "the first click reports")
    catch.dispatcher()
    catch.dispatcher()
    check(#calls.execs == 1, "further clicks in the same second are dropped")
    now = now + 1
    catch.dispatcher()
    check(#calls.execs == 2, "a click a second later reports again")

    if calls.execs[1] then
        check(calls.execs[1]:find("/api/run/click", 1, true) ~= nil, "the click posts to /api/run/click")
        check(calls.execs[1]:find("--max-time", 1, true) ~= nil, "a wedged listener cannot pile up curls")
    end
end

if failures > 0 then
    io.stderr:write(failures .. " check(s) failed\n")
    os.exit(1)
end
print("df-hud.lua: all checks passed")
