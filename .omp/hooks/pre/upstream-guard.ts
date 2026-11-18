// Project hook: refuse agent writes to GitHub repositories the user does not
// own. The policy lives in tools/agent-hooks/upstream-guard.js, which Claude
// Code runs as a PreToolUse command as well, so both agents obey one rule.
import guard from "../../../tools/agent-hooks/upstream-guard.js";

type Verdict = { block: true; reason: string };
type ToolCall = (event: unknown, ctx: unknown) => Promise<Verdict | undefined>;
type HookApi = { on(event: "tool_call", handler: ToolCall): void };

// field reads one property of an event object the runtime hands over untyped.
function field(value: unknown, key: string): unknown {
	if (typeof value !== "object" || value === null) return undefined;
	return (value as Record<string, unknown>)[key];
}

export default function (pi: HookApi): void {
	pi.on("tool_call", async (event, ctx) => {
		if (field(event, "toolName") !== "bash") return;
		const input = field(event, "input");
		const command = String(field(input, "command") ?? "");
		const cwd = String(field(input, "cwd") ?? field(ctx, "cwd") ?? process.cwd());
		const verdict: Verdict | null = guard.evaluate(command, cwd);
		if (!verdict) return;
		return { block: true, reason: verdict.reason };
	});
}
