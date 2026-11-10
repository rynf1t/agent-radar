-- radar for Rex: one key floats the agent radar over whatever session you
-- are looking at. Enter jumps to an agent and closes it, esc closes it, and
-- the same key closes it too.
--
-- Link this file into your Rex config and load it from init.lua:
--
--   ln -s ~/path/to/agent-radar/rex/radar.lua ~/.config/rex/radar.lua
--
--   local radar = dofile(os.getenv("HOME") .. "/.config/rex/radar.lua")
--   radar.setup{ key = "cmd+shift+u" }
--
-- setup takes key (false for no key, the action stays in the command
-- palette), binary (the radar to run, ~/go/bin/radar by default) and bounds
-- ({x, y, width, height} as fractions of the window).

local M = {}

local TERMINAL = "com.superlogical.terminal.shell"
local open = {} -- session id to { block = the popup, back = the pane it covers }

-- The window holding block_id, else the session's active window.
local function window_of(view, block_id)
  local active
  for _, win in ipairs(view.windows or {}) do
    if win.window_id == view.active_window_id then active = win end
    for _, layer in ipairs(win.layers or {}) do
      for _, b in ipairs(layer.blocks or {}) do
        if block_id and (type(b) == "table" and b.block_id or b) == block_id then return win end
      end
    end
  end
  return active or (view.windows or {})[1]
end

-- close shuts the session's popup and reports whether one was open. A popup
-- that already closed itself after a jump fails to close, which counts as
-- not open.
local function close(session)
  local p = open[session]
  open[session] = nil
  if not p then return false end
  local ok, res = pcall(rex.call, "block.close", { session_id = session, block_id = p.block })
  if not (ok and res ~= nil) then return false end
  -- Rex focuses some other pane when a float closes. Go back.
  if p.back then pcall(rex.call, "session.focus_block", { session_id = session, block_id = p.back }) end
  return true
end

function M.setup(opts)
  opts = opts or {}
  local home = os.getenv("HOME") or ""
  local bin = opts.binary or (home .. "/go/bin/radar")
  local bounds = opts.bounds or { 0.22, 0.1, 0.56, 0.8 }
  local key = opts.key
  if key == nil then key = "cmd+shift+u" end

  rex.action{ name = "radar_popup", title = "Agent Radar",
    run = function(ctx)
      local session = ctx and ctx.session_id
      if not session or close(session) then return end
      local ok, view = pcall(rex.session.view, { session_id = session })
      if not (ok and type(view) == "table") then return end
      local win = window_of(view, ctx.block_id)
      if not (win and win.window_id) then return end
      local back = win.focused_block_id or ctx.block_id
      local command = { bin, "view", "--popup" }
      if back then
        command[#command + 1] = "--back"
        command[#command + 1] = back
      end
      local created = rex.call("session.new_layer", {
        session_id = session, window_id = win.window_id, bounds = bounds, focus = true,
        layout = rex.layout.block{
          flavor = TERMINAL,
          options = { command = command, shell = "none", cwd = home },
        },
      })
      if created and created.block_ids then
        open[session] = { block = created.block_ids[1], back = back }
        rex.session.set_block_label{ session_id = session, block_id = created.block_ids[1], label = "Radar" }
      end
    end }

  if key then rex.bind(key, "radar_popup") end
end

return M
