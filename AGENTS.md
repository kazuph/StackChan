# StackChan Repo Rules

- Think and report in Japanese.
- Do not use the legacy Python bridge for StackChan voice, IR reception, IR decode speech, or IR send paths.
- Use the Go bridge under `server/bridgego/` and Go commands under `server/cmd/` for StackChan bridge and IR remote work.
- The old Python files under `server/bridge/` and `tools/mac_ac_remote/ac_ir_tool.py` are legacy reference only. Do not start them, depend on them, or add new behavior to them.
- Keep machine-local endpoints and secrets in ignored `.env` files. Do not commit them.
- Before reporting StackChan bridge or IR remote changes as done, verify the Go bridge is listening on port 8787 and that `/mcp/list` reaches an active device session.
