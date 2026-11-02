// cs-shell-bound for OpenCode: raise the default bound on one shell call.
//
// OpenCode bounds a foreground shell call at 120 seconds unless the model passes a timeout, and
// reads no variable or setting that moves that default. An orchestrator's wait blocks for a whole
// chunk, so cs-campaign installs this plugin in an OpenCode orchestrator's profile and sets
// CS_OPENCODE_SHELL_TIMEOUT_MS. A call that names its own timeout, or runs in the background,
// keeps what it asked for.
//
// The plugin runs in the server process, so the variable is the server's, which a member's
// sandbox-wide environment reaches. Unset or not a positive number, the plugin registers nothing.
//
// opencode turns an exception thrown by a hook into a failed tool call, so the hook catches its own.
const BOUND = Number(process.env.CS_OPENCODE_SHELL_TIMEOUT_MS ?? "0")

export default {
  id: "cs.shell-bound",
  async setup(ctx: any) {
    if (!(BOUND > 0)) return
    await ctx.tool.hook("execute.before", async (event: any) => {
      try {
        const input = event?.input
        if (event?.tool !== "shell" || !input || typeof input !== "object") return
        if (input.timeout === undefined && !input.background) input.timeout = BOUND
      } catch (e) {
        console.error(`cs-shell-bound: ${String(e).slice(0, 200)}`)
      }
    })
  },
}
