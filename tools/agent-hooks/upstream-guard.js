// Upstream guard: blocks agent writes to GitHub repositories the user does not
// own, unless they said so. Reads stay allowed, so inspecting an upstream PR or
// issue keeps working; creating, commenting, merging, pushing and releasing do
// not, since those are what "do not touch upstream" means here.
//
// One module serves both hook runtimes:
//   - omp:     .omp/hooks/pre/upstream-guard.ts imports evaluate() and blocks
//              the bash tool call with its reason.
//   - Claude:  .claude/settings.json runs this file as a PreToolUse command,
//              which reads the tool call as JSON on stdin and prints a deny.
//
// The allowlist is the single knob. Writes to a repository whose owner is not
// listed are refused; so are writes whose repository cannot be determined,
// because that is a guess in the wrong direction.
"use strict";

const { execFileSync } = require("child_process");

// GitHub owners whose repositories the agent may write to.
const ALLOWED_OWNERS = new Set(["si-mods"]);

// gh commands that change something, by group. A group listed here that is
// called with any verb below is a write; every other verb of that group reads.
const GH_WRITE_VERBS = {
	pr: new Set(["create", "merge", "close", "reopen", "edit", "comment", "review", "ready", "lock", "unlock", "convert", "revert", "update-branch"]),
	issue: new Set(["create", "close", "reopen", "edit", "comment", "delete", "transfer", "pin", "unpin", "lock", "unlock", "develop"]),
	release: new Set(["create", "upload", "edit", "delete", "delete-asset"]),
	repo: new Set(["fork", "edit", "rename", "archive", "unarchive", "delete", "transfer", "set-default", "sync"]),
	workflow: new Set(["run", "enable", "disable"]),
	run: new Set(["cancel", "delete", "rerun"]),
	secret: new Set(["set", "delete"]),
	variable: new Set(["set", "delete"]),
};

// gh api flags that make the request a write, whatever the method says.
const API_WRITE_FLAGS = /(?:^|\s)(?:-X|--method)(?:\s+|=)(?:POST|PATCH|PUT|DELETE)\b|(?:^|\s)(?:-f|--field|-F|--raw-field|--input)(?:\s+|=)/;

// A remote URL or owner/name, as git and gh accept them.
const REPO_FORMS = [
	/^https?:\/\/github\.com\/([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/,
	/^git@github\.com:([^/\s]+)\/([^/\s]+?)(?:\.git)?$/,
	/^github\.com\/([^/\s]+)\/([^/\s]+?)(?:\.git)?$/,
	/^([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+)$/,
];

// parseRepo reads a GitHub repository out of owner/name, a URL or a git remote.
function parseRepo(text) {
	const s = String(text || "").trim();
	for (const re of REPO_FORMS) {
		const m = re.exec(s);
		if (m) return { owner: m[1], name: m[2] };
	}
	return null;
}

// remoteRepo reads the repository a git remote of dir points at.
function remoteRepo(dir, remote) {
	const run = (args) => {
		try {
			return execFileSync("git", args, { cwd: dir, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"], timeout: 5000 }).trim();
		} catch {
			return "";
		}
	};
	for (const url of [run(["-C", dir, "remote", "get-url", "--push", remote]), run(["-C", dir, "remote", "get-url", remote])]) {
		const repo = parseRepo(url);
		if (repo) return repo;
	}
	return null;
}

// tokens splits a command the way a shell would for the flags we look at: the
// pieces between quotes stay whole, quotes come off, redirections and pipes cut
// it. It is not a shell parser; anything it cannot read it leaves as is, and the
// command is refused rather than guessed at.
function tokens(command) {
	const out = [];
	let cur = "";
	let quote = null;
	let seen = false;
	for (const ch of command) {
		if (quote) {
			if (ch === quote) quote = null;
			else cur += ch;
			continue;
		}
		if (ch === '"' || ch === "'") {
			quote = ch;
			seen = true;
			continue;
		}
		if (/\s/.test(ch)) {
			if (seen) out.push(cur);
			cur = "";
			seen = false;
			continue;
		}
		if (ch === "|" || ch === ";" || ch === "\n") {
			if (seen) out.push(cur);
			return out;
		}
		cur += ch;
		seen = true;
	}
	if (seen) out.push(cur);
	return out;
}

// cwdOf follows a leading `cd <dir> &&` to the directory the rest runs in, since
// the hook sees the command, not the shell's final working directory.
function cwdOf(command, cwd) {
	const m = /^\s*cd\s+(?:"([^"]+)"|'([^']+)'|(\S+))\s*(?:&&|;)/.exec(command);
	const dir = m && (m[1] || m[2] || m[3]);
	if (!dir) return cwd;
	if (/^[A-Za-z]:[\\/]/.test(dir) || dir.startsWith("/") || dir.startsWith("\\\\")) return dir;
	return require("path").resolve(cwd, dir);
}

// flagValue returns the value of --name/-n in tokens, written either way.
function flagValue(toks, names) {
	for (let i = 0; i < toks.length; i++) {
		for (const name of names) {
			if (toks[i] === name) return toks[i + 1] || "";
			if (toks[i].startsWith(name + "=")) return toks[i].slice(name.length + 1);
		}
	}
	return "";
}

// ghTarget names the repository a gh call writes to: --repo, or the path of an
// api call, or else the remote of the directory it runs in.
function ghTarget(toks, dir) {
	const repo = flagValue(toks, ["--repo", "-R"]);
	if (repo) return parseRepo(repo);
	const api = toks.indexOf("api");
	if (api >= 0) {
		const path = toks.slice(api + 1).find((t) => t.startsWith("repos/"));
		if (path) return parseRepo(path.slice("repos/".length));
	}
	return remoteRepo(dir, "origin");
}

// ghWrite reports whether the gh command changes something upstream, and which
// repository it targets.
function ghWrite(toks) {
	const at = toks.indexOf("gh");
	if (at < 0) return null;
	const args = toks.slice(at + 1);
	const group = args[0];
	if (!group) return null;
	if (group === "api") {
		return API_WRITE_FLAGS.test(" " + args.join(" ")) ? { group, verb: "request" } : null;
	}
	const verbs = GH_WRITE_VERBS[group];
	if (!verbs) return null;
	const verb = args[1];
	if (!verb || !verbs.has(verb)) return null;
	return { group, verb };
}

// gitPushTarget names the repository a git push writes to.
function gitPushTarget(toks, dir) {
	const at = toks.indexOf("git");
	if (at < 0) return null;
	let i = at + 1;
	let workdir = dir;
	while (i < toks.length) {
		if (toks[i] === "-C" && toks[i + 1]) {
			workdir = require("path").resolve(dir, toks[i + 1]);
			i += 2;
			continue;
		}
		if (toks[i].startsWith("-")) {
			i += 1;
			continue;
		}
		break;
	}
	if (toks[i] !== "push") return null;
	const rest = toks.slice(i + 1).filter((t) => !t.startsWith("-"));
	// push has no remote argument: it pushes to the branch's upstream.
	const remote = rest[0] || "origin";
	const direct = parseRepo(remote);
	if (direct) return { repo: direct, remote };
	return { repo: remoteRepo(workdir, remote), remote };
}

// evaluate decides one bash command. It returns null when the command may run,
// and { block, reason } when it may not.
function evaluate(command, cwd) {
	const cmd = String(command || "");
	if (!/\bgh\b|\bgit\b/.test(cmd)) return null;
	const dir = cwdOf(cmd, cwd || process.cwd());
	const toks = tokens(cmd);

	const push = gitPushTarget(toks, dir);
	if (push) {
		const repo = push.repo;
		if (!repo) {
			return { block: true, reason: `Upstream guard: cannot tell which repository "git push" writes to (remote "${push.remote}" unreadable). Name the remote's owner/repository and ask the user first.` };
		}
		if (ALLOWED_OWNERS.has(repo.owner)) return null;
		return { block: true, reason: `Upstream guard: refusing to push to ${repo.owner}/${repo.name}. Only ${[...ALLOWED_OWNERS].join(", ")} are writable; the user forbade touching upstream repositories without saying so. Ask them.` };
	}

	const write = ghWrite(toks);
	if (!write) return null;
	const repo = ghTarget(toks, dir);
	if (!repo) {
		return { block: true, reason: `Upstream guard: cannot tell which repository "gh ${write.group} ${write.verb}" writes to. Pass --repo owner/name after the user approves it.` };
	}
	if (ALLOWED_OWNERS.has(repo.owner)) return null;
	return { block: true, reason: `Upstream guard: refusing "gh ${write.group} ${write.verb}" on ${repo.owner}/${repo.name}. Only ${[...ALLOWED_OWNERS].join(", ")} are writable; ask the user before touching an upstream repository.` };
}

// main is the Claude Code PreToolUse contract: JSON on stdin, a decision on
// stdout. Any failure stays silent, which lets the call through rather than
// blocking work on a broken hook.
function main() {
	let raw = "";
	process.stdin.on("data", (d) => (raw += d)).on("end", () => {
		let input;
		try {
			input = JSON.parse(raw);
		} catch {
			return;
		}
		const name = input.tool_name || input.toolName || "";
		if (!/^(?:bash|powershell)$/i.test(name)) return;
		const params = input.tool_input || input.input || {};
		const verdict = evaluate(params.command || "", params.cwd || process.cwd());
		if (!verdict) return;
		process.stdout.write(JSON.stringify({
			hookSpecificOutput: {
				hookEventName: "PreToolUse",
				permissionDecision: "deny",
				permissionDecisionReason: verdict.reason,
			},
		}));
	});
}

module.exports = { evaluate, parseRepo };

if (require.main === module) main();
