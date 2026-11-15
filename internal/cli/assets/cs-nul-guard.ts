// cs-nul-guard for OpenCode: fail a shell call whose command holds a NUL byte.
//
// A process cannot be given an argument that holds a NUL, so spawning such a command throws at
// once. OpenCode 2 reads that throw as a fault in itself rather than as the tool's error, and
// leaves the call running for good. The seat then waits on a call that never returns. cs-campaign
// installs this plugin in every OpenCode member's profile, so the call fails before the spawn,
// and the agent reads why and can write the byte another way.
//
// opencode turns an exception thrown by a hook into a failed tool call, which is the point here.
export default {
  id: "cs.nul-guard",
  async setup(ctx: any) {
    await ctx.tool.hook("execute.before", async (event: any) => {
      const cmd = event?.input?.command
      if (typeof cmd === "string" && cmd.includes("\u0000")) {
        throw new Error(
          "The command contains a NUL byte (\\u0000), which cannot be passed to a process. " +
            "Write the character another way, e.g. chr(0) in Python or printf '\\\\0' in the shell.",
        )
      }
    })
  },
}
