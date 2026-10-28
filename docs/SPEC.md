# Roar specification

Owner: Viz. Updated: 2026-10-28. Formerly named Ping; renamed to Roar on 2026-10-27, because `ping` shadowed the system's network `ping`. Status: implemented on `main`; the spec itself is not yet ratified.

## Problem and solution

Agents in different terminal panes need to send each other messages.
Typing into another pane is unsafe: it can land in a draft, a shell, or a permission prompt.
Queued terminal bytes also prove nothing about whether the agent read them.

Roar is a small set of commands that routes messages between Rex panes.
It delivers through each agent's native channel when one is connected.
When no native channel exists, it types through Rex, but only after positive proof that the pane holds an agent with an empty composer.
Otherwise it holds the message.

> **Core insight: a message is delivered only when the provider says so.** Every other signal (a file written, a socket write, queued bytes, a visible pane) only moves a message closer. Roar never sends a message a second time by another path while an earlier attempt has an unknown outcome.

## Scope

| In scope | Out of scope |
| --- | --- |
| Messages between Rex panes, on one host or across Rex hosts | A separate agent identity or address registry |
| Native delivery adapters for Claude Code and Codex | Task coordination and issue launching (deferred) |
| Terminal-input fallback through Rex, with readiness proof | Credentials and accounts (separate tools) |
| A durable per-host message store | Roles, delegation, broadcast |
| `roar setup`, which adds Roar to each agent's default config | Wrapping or launching agents; service installation |
| Reading agent turn state from Rex program status | Deriving agent state from terminal polling, screen scraping, or hooks. A Codex hook only names the pane's thread. |

Rex supplies identity. A pane is a Rex block on a Rex server. Roar adds no names of its own.

## Design principles

- **Commands, not a service.** Every command works on its own. There is no Roar daemon.
- **The store is the interface.** Commands share state through files in one directory per host. File locks serialize changes.
- **No wrappers.** The operator starts `claude` and `codex` as usual. `roar setup` puts Roar in their default config, so every session gets it.
- **Long-lived only where the provider demands it.** The Claude adapter lives as long as Claude does. Codex delivery runs inside the sending command. `roar deliver --watch` is optional.
- **Plain output.** Text by default, `--json` for scripts. Exit codes carry the outcome.

Implementation language: Go.

## Commands

| Command | Does | Exit codes |
| --- | --- | --- |
| `roar send <target> [text]` | Stores a message for the target, then runs one delivery pass for it. Reads the body from stdin when no text is given. Prints `<id> <state> [hold reason]`, after waiting up to 3 s while a native adapter delivers. Scripts take the first field or use `--json`. `--request` sets `kind: request`; `--reply <id>` answers a message in the caller's inbox; `--key <k>` makes a retried send store once. | 0 stored, 2 refused |
| `roar inbox [--all]` | Lists unread messages addressed to the caller's own pane. `--all` includes read and expired ones. A read peeks. | 0 |
| `roar read <id> [--mark]` | Prints one message. `--mark` marks it read; only the recipient pane may do that. | 0, 2 |
| `roar status [id...]` | Shows the state of messages the caller sent, or of the given IDs. | 0 |
| `roar withdraw <id>` | Expires the caller's own unread message. | 0, 2 |
| `roar resolve <id> --read\|--requeue` | Settles an `unknown` message after the operator checked the conversation. | 0, 2 |
| `roar ready <target>` | Prints the fallback readiness verdict for a pane. It takes the same targets as `roar send`, host prefix included. | 0 empty, 1 wait, 2 refuse |
| `roar deliver [target...] [--watch]` | Runs fallback delivery for queued messages whose recipients have no native adapter. `--watch` follows `rex events` and retries on status changes. | 0, 2 |
| `roar enqueue --sha256 <digest>` | Plumbing for cross-host sends: reads a forwarded envelope from stdin, stores it, then runs one delivery pass. | 0 stored or duplicate, 2 refused, 6 digest mismatch |
| `roar setup` | Adds Roar to every Claude and Codex config root on this host. See [Setup](#setup). Safe to run again. | 0, 2 |
| `roar install <host>` | Copies this binary to another Rex host's `~/.local/bin`, then runs `roar setup` there. | 0, 2 |
| `roar mcp` | Claude adapter. Claude starts it; operators never run it. | |
| `roar hook codex` | Codex `SessionStart` hook. Records which thread the pane shows. Codex runs it; operators never do. | always 0 |

Exit code 2 always comes with one line on stderr that names the reason, for example `ambiguous_target`, `no_roar_on_host`, or `limit_exceeded`.

## Addressing

A recipient is one Rex block: `(server, session_id, block_id)`.

- Rex IDs resolve only on their own server. A block ID sent to another server returns "not found".
- Store the server as its `host_id` plus endpoint URL from `rex whoami --json`. Host labels are local nicknames and differ between hosts.
- Store the server's `instance_id` too. A changed `instance_id` means the server restarted, and stored block IDs are invalid.
- A target is a full block ID, `[host-label:]session-label[/pane]`, `[host-label:]pane@session-label`, or `[host-label:]name`. `pane@session` is the same as `session/pane`; it reads like the tab name people see. `roar send` resolves the target once and stores IDs.
- A `host-label:` prefix names a host from `rex hosts`. Rex gives the local host an empty label, so this machine's own name (the host name without its domain, any case) also counts and means local. An address copied from another host's message then works when sent back from this host. A remote label wins over the machine's name.
- A pane is a pane label, a window label, or the last six or more characters of a block ID. A pane label wins over a window label. A window label names every terminal pane in that window.
- When no label matches, a pane can be a hint instead. A label always wins over a hint, so an operator can still name a pane `left` or `codex`.

| Hint | Names |
| --- | --- |
| `claude`, `codex` | The panes whose foreground program is that agent, by the same test the readiness proof uses |
| `left`, `right`, `top`, `bottom` | The panes on that edge of their window's tiled layout |
| `top-left`, `top-right`, `bottom-left`, `bottom-right` | The panes on both edges |
| `peer`, alone | The other panes in the sender's own window. It takes no session or host, because it is relative to the sender. With two side-by-side panes it names the other one. |

  Positions come from the layout rectangles `rex window ls` reports, so they match what the operator sees. In two side-by-side panes, `left` and `right` each name one pane and `top` names both. A hint applies in every window of the session; a session with several tabs can make it ambiguous.
  Example: a session `roar` with Claude on the left and Codex on the right. `codex@roar` and `right@roar` both name the Codex pane.
- A name that matches no session label is looked up as a window or pane label in every session. People see window labels as Rex tab names, not session labels, so they and their agents address by them. The name must occur in one session only; otherwise the send is refused with `ambiguous_target` and lists each `name@session`.
- When a target names several panes, the only agent pane wins, by foreground process. Otherwise the send is refused with `ambiguous_target` and the candidates are listed.

### Sender identity

`roar send` reads `REX_SESSION` and `REX_BLOCK` from its own environment and `rex whoami --json` for the host.
Outside a Rex pane it refuses with `not_in_rex_pane`.

Every pane runs as the same user. This identity prevents mistakes. It is not authentication.

## The envelope

```json
{
  "id": "rm-01JB...",
  "from": {"host_id": "...", "session": "session:...", "block": "block:..."},
  "to":   {"host_id": "...", "session": "session:...", "block": "block:..."},
  "kind": "inform",
  "text": "...",
  "reply_to": "rm-...",
  "idempotency_key": "...",
  "created_at": "2026-10-27T12:00:00Z",
  "forwarded": false
}
```

| Field | Rule |
| --- | --- |
| `id` | ULID with a `rm-` prefix. With an `idempotency_key`, the ID derives from `sha256(host_id, from.block, key)`, so a retried send gets the same ID. |
| `kind` | `inform` or `request`. Unknown values become `inform`. |
| `reply_to` | Optional. `roar send --reply <id>` reads the original envelope from the caller's own inbox and addresses its sender. |
| `forwarded` | True when the envelope arrived from another host. A forwarded envelope is never forwarded again. |
| `files` | Optional. Name, size, and SHA-256 of each attachment. See [Attachments](#attachments). |

The agent sees one header line, then the body: `[roar <id> from <session-label>/<block-short> <kind> ↩<reply_to>]`.
The header carries no usage hint. The [agent skill](#agent-skill) teaches the agent to answer, so no message pays for instructions.
Roar strips control characters and newlines from every header field.

## Attachments

`roar send --file PATH` attaches a file. The flag repeats. The files travel with the message and are on the recipient's host before the agent reads the message.

| Rule | Detail |
| --- | --- |
| Size | 16 MiB in total per message. A larger send is refused with `limit_exceeded` before anything is stored. |
| Names | The file's base name. A name that repeats gets a numeric suffix. Names with `/`, a leading `.`, or more than 128 bytes are refused. |
| Envelope | `files` lists each attachment's name, size, and SHA-256. The contents are not in the envelope. |
| Storage | `box/<block>/<id>/files/<name>`, written into the staging directory and renamed with the message, so a message never appears without its files. |
| What the agent sees | One line per file after the header: `attached: <absolute path on the recipient's host> (<size> bytes)`. |
| Across hosts | The stream carries the envelope and the file contents together. The receiver checks every file's size and SHA-256 before storing. |
| Lifetime | Files are removed with their message when it is pruned, 7 days after it is finished. |

## The store

One directory per host: `$ROAR_HOME`, default `$XDG_STATE_HOME/roar` (`~/.local/state/roar`). Mode 0700.

```text
$ROAR_HOME/
  box/<recipient block>/<message id>/
    envelope.json      written once, never changed
    state.json         replaced atomically on every change
    lock               flock target for this message
  ids/<message id>        symlink to the box entry, for lookup by ID
  sent/<message id>.json  the envelope without its body, for the sender's status
  adapters/<block>.lock   held by a live native adapter
  tmp/                    staging for new messages, on the same file system
```

The `ids/` link is written after the rename into `box/`. A crash between the two leaves only the box entry, so lookup falls back to searching `box/*/<id>`.
`sent/` keeps no body. The recipient's host may be another machine, so the sender keeps only routing details.

| Operation | How |
| --- | --- |
| Store a message | Create the message directory in a temporary name, write both files, fsync, then rename into `box/`. An existing directory with the same ID means duplicate, which is success. |
| Change state | Take the message's `flock`. Write `state.json` to a temporary file, fsync, rename, fsync the directory. Release. |
| Claim for delivery | Hold the message's `flock` for the whole attempt. Another deliverer that finds the lock held skips the message. |
| Is a native adapter live? | Try a non-blocking `flock` on `adapters/<block>.lock`. If it succeeds, no adapter holds it; release at once. The kernel releases the lock when an adapter dies, so a crash never leaves a stale claim. |
| Wake an adapter | Adapters poll their `box/<block>/` directory every 250 ms. A directory read is cheap, and polling needs no platform file watcher. |

Retention: unread messages are never pruned. Read and expired messages are kept 7 days, so a late reply can still find its original. Pruning runs during `roar send` and `roar enqueue`.

Limits: 256 KiB per message, 2 MiB of unread messages per recipient, 16 MiB per host. A send over a limit is refused before anything is stored.

## Agent activity from Rex

Rex tracks each agent's turn state through its program status protocol (OSC 7501).
Claude Code and Codex both report it natively. No hook setup is needed.
Roar reads it with the block's `program_status` method and watches `program_status_changed` in `rex events`.

| Status | Meaning for Roar |
| --- | --- |
| `working` | Mid-turn |
| `done` | The turn finished. It stays `done` until the next input. Roar treats it like `idle`. |
| `idle` | At rest |
| `blocked` | Waiting on a modal, such as a permission prompt (`kind` says which) |
| No record | Starting up, in a dialog before the agent reports, or not an agent |

A record counts only when its owner's PID and name match the block's foreground process. The system reuses PIDs, and a killed agent leaves a stale record behind.

Rex program status is Roar's only source of agent turn state.
Roar reads Claude transcripts for one purpose, message receipt, and never for turn state.

Roar uses the status in four places:

1. The Claude adapter holds a `request` until the status is `idle` or `done`.
2. Adapters and `roar deliver --watch` retry held messages on `program_status_changed`, with no polling loop.
3. It is the first gate of the fallback proof.
4. It confirms a fallback submit: `working` after Enter.

Status alone cannot prove an empty composer. A typed draft leaves the status `idle`, which was verified live. So the fallback still checks the composer row.

## Delivery

### Choosing a path

| Recipient state | Path |
| --- | --- |
| A native adapter holds `adapters/<block>.lock` | Native. Only the adapter delivers. |
| The pane has a live [Codex binding](#binding-1) and its thread is loaded on the Codex daemon | Native, by whichever command holds the message lock |
| No native adapter, and the readiness proof says empty | Fallback, by whichever `roar send`, `roar enqueue`, or `roar deliver` holds the message lock |
| No native adapter, and the proof says busy, draft, modal, or unknown | Hold. `roar deliver --watch` or the next send retries. |
| The pane runs something that is not an agent, such as a shell | Hold with `wrong_program`. Roar never types into it; an agent may start there later, and a person can still read the message with `roar inbox`. |
| The block is gone or its program exited | Expire with `dead_pane` |
| An earlier attempt for this message has an unknown outcome | Hold. Nothing tries again until `roar resolve`. |

A pane can gain or lose its native adapter. Roar decides per attempt, never per message.
Without `roar deliver --watch` running on the recipient's host, a held fallback message waits for the next send to that pane or a manual `roar deliver`.

### States

| State | Entered when | Next |
| --- | --- | --- |
| `queued` | The message directory was renamed into `box/` | `written`, `submitting`, `expired` |
| `written` | A native adapter handed the message to the provider | `accepted`, `read`, `queued`, `unknown` |
| `accepted` | Claude recorded the channel message as enqueued | `read`, `queued` (removed without receipt) |
| `read` | Provider receipt proof arrived, or the recipient ran `roar read --mark` | Final |
| `submitting` | The fallback began typing | `submitted`, `unknown` |
| `submitted` | Enter was sent and Rex reported the agent `working` | Final |
| `unknown` | A provider call or a typing sequence ended without a result | `read` or `queued`, by `roar resolve` only |
| `expired` | The recipient block ended or changed program, or the sender withdrew the message | Final |

`held` and `blocked` are views, not states.
`held` is a `queued` message with a hold reason in `state.json`.
`blocked` is a `written` message older than 30 seconds whose adapter is still live.

`read` and `submitted` differ on purpose. `read` comes from the provider. `submitted` only proves that our Enter started a turn.

Reaching `read` or `submitted` clears `reason`. An earlier retry's error no longer describes a delivered message, and a sending agent that sees one spends a turn explaining it.

### Receipt detail

Each `read` records its proof and its boundary.

| Proof | Boundary |
| --- | --- |
| `claude_transcript` | `turn` (the message started a turn) or `mid_turn` (Claude absorbed it inside a running turn) |
| `codex_turn_start` | `turn` |
| `codex_turn_steer` | `mid_turn` |
| `marked` | None. The recipient ran `roar read --mark`. |

The boundary matters. Claude shows a mid-turn message to the model as untrusted information, not as an instruction.

### Request timing

The Claude adapter writes a `request` only when Rex reports the agent `idle` or `done`.
It writes an `inform` at any time.
If Claude starts a turn between the check and the write, the receipt records `mid_turn` and the sender sees it.

Codex has no such gate. Its steer adds user input to the running turn.

### Recovery after a crash

An adapter that starts takes `adapters/<block>.lock`, then settles messages left in flight for its block:

| Left in state | Claude adapter | Codex adapter |
| --- | --- | --- |
| `written` | Re-scan the transcript. Apply any `accepted` or `read` record. Otherwise return to `queued`. | `unknown`. The turn may have run. |
| `accepted` | Keep it while the same Claude process lives. Return it to `queued` after a remove record with no receipt. | Not used |

A `submitting` message whose lock is free belongs to a deliverer that died. Any command that finds one sets it to `unknown`.

## Setup

**Roar lives in the agents' default config, not in a wrapper.** The operator runs `claude` and `codex` the usual way, through whatever launcher they already use.

`roar setup` finds config roots in the home directory and changes each one:

| Root | Found as | Changes |
| --- | --- | --- |
| Claude | `~/.claude` if it exists, `$CLAUDE_CONFIG_DIR`, and every `~/.claude-*` directory that holds `.claude.json` | The skill. A user-scope MCP server `roar` that runs `<roar> mcp`, added with `claude mcp add-json --scope user`. |
| Codex | `~/.codex` if it exists, `$CODEX_HOME`, and every `~/.codex-*` directory that holds `config.toml` | The skill. One `SessionStart` group in `hooks.json` that runs `<roar> hook codex`. |

- `<roar>` is the absolute path of the running binary, so agents need no `PATH` entry for it.
- Setup reads the current config first and changes only what differs. A second run prints `ok` for each root.
- It appends its hook group after the existing groups and never reorders groups or keys. Codex keys hook trust by position, so earlier trust stays valid. A moved binary replaces Roar's own group in place.
- It runs the `claude` binary from `PATH`, else `~/.local/bin/claude`, never a shell function that wraps it.
- For `~/.claude`, setup runs `claude mcp` without `CLAUDE_CONFIG_DIR`, so the server lands in `~/.claude.json`. For any other root it sets `CLAUDE_CONFIG_DIR`.
- A root that fails prints one line on stderr. Setup continues with the next root and exits 2 at the end.

Two steps stay with the operator, because each one is a consent the provider asks for:

| Step | Why Roar cannot do it |
| --- | --- |
| Start Claude with `--dangerously-load-development-channels server:roar`, for example in the shell function that already starts `claude` | Claude loads channels only from the launch flag. No setting turns one on. |
| Run `/hooks` once in each Codex home and trust the Roar hook | Codex trust-gates new hooks. Writing the trust entry would bypass that gate. |

Without either step, the pane still gets messages through [fallback](#fallback-through-rex).

`roar install <host>` copies the binary to another Rex host, then runs `roar setup` there.

## Agent skill

**An agent learns Roar from a skill, not from adapter instructions.** Claude Code and Codex list every installed skill's name and description in context, and load the body when the description matches.

| Need | Covered by |
| --- | --- |
| Answering a message that just arrived | The skill. Its description names the `[roar <id> from ...]` header as its trigger. |
| Sending on the agent's own initiative ("tell api-work the schema merged") | The skill: targets, `--request`, `--file`, `roar ready`, `roar status`. |

The skill is one `SKILL.md`, embedded in the binary. Both tools read the same format.

### Installation

[`roar setup`](#setup) writes the skill to `<root>/skills/roar/SKILL.md` in every config root it finds.

| Existing `skills/roar` | Action |
| --- | --- |
| Missing | Write it |
| A `SKILL.md` Roar wrote (frontmatter `metadata.generated-by: roar`) | Rewrite it when the content differs |
| A symlink, or a `SKILL.md` without the marker | Leave it. The operator manages it. |

**Keep the skill small.** An agent loads it for every message it answers, so every line costs on each reply. It stays near 300 tokens: the header fields, four command forms, and what `roar send` prints. `roar send` reports the delivery outcome itself, so the skill tells the agent not to follow up with `roar status`.


## Claude adapter

### Launch

[`roar setup`](#setup) registers `roar mcp` as a user-scope MCP server. Claude starts it in every session.
The channel loads only when Claude starts with `--dangerously-load-development-channels server:roar`. Claude accepts several servers in that list, for example `server:penfold server:roar`.
Claude starts MCP servers only after the operator accepts the development-channel consent dialog. Roar does not answer that dialog.

### Binding

`roar mcp` inherits `REX_SESSION` and `REX_BLOCK` from the pane.
Its parent is the Claude process. Before declaring the channel, it reads the parent's argv and requires the exact `server:roar` channel flag and no print mode.
Without that proof it declares no channel, takes no adapter lock, and logs `channel_flag_missing`. The pane then falls back to terminal delivery.

The check runs every time `roar mcp` starts. A provider self-update can restart Claude without the flag.

### Notification

```json
{"jsonrpc":"2.0","method":"notifications/claude/channel",
 "params":{"content":"<header and body>","meta":{"id":"rm-...","sender":"<from>"}}}
```

Claude renders this as `<channel source="roar" id="..." sender="...">` around the content.
Roar validates meta before writing, because Claude drops a bad notification silently:

- every value is a string;
- every key is an identifier;
- no key is `source`, which would duplicate the source attribute.

### Receipt from the transcript

Find the transcript from `$CLAUDE_CONFIG_DIR/sessions/<claude pid>.json`, where the PID is `roar mcp`'s parent.
That file holds the current `sessionId` and `cwd`. It follows `/clear`. The child's `CLAUDE_CODE_SESSION_ID` environment variable does not.

| Transcript record | Meaning |
| --- | --- |
| `queue-operation` `enqueue` with the envelope | `accepted` |
| `user`, `isMeta`, `origin.kind=channel`, `origin.server=roar` | `read`, boundary `turn` |
| `queue-operation` `remove` with `reason=absorbed_mid_turn`, then `attachment` `queued_command` with the same origin | `read`, boundary `mid_turn` |
| `remove` with no receipt after a 2-second grace and one re-scan | back to `queued` |

Match the message ID by parsing the opening `<channel>` tag. Reject a tag with duplicate attributes.
Read only records written after the message was written, and only in this Claude process's own transcript.

These records are undocumented provider formats. If the sessions file or the records are missing, the message stays `accepted` or `written` and `roar status` shows why. Roar never guesses receipt.

### Replay

Replay an `accepted` message only when the `roar mcp` process that wrote it has changed. Same writer means Claude still holds it.
Each adapter records its writer identity (`mcp:<pid>:<start time>`) and the write time on every message it writes. It records the write before it writes, so a crash never leaves a written message looking queued.
Only `queued` messages are written, so one adapter never writes the same ID twice. A second write happens only after a removal without receipt, or after the writer changed. Both are intended replays.

The adapter's MCP `instructions` say one thing: messages arrive as `roar` channel blocks, and the header and the [agent skill](#agent-skill) say how to answer.

## Codex adapter

### Why the daemon

Codex runs its TUI as a client of a shared App Server daemon per Codex home. The daemon serves `<codex home>/app-server-control/app-server-control.sock`.
Any process of the same user can connect to that socket. A `turn/start` or `turn/steer` from that second client renders in the TUI and the model answers it.
So Roar needs no proxy and no wrapper. It needs only to know which thread the pane shows.

### Binding

The `SessionStart` hook names the thread. Codex runs it with the TUI's environment, so it carries `REX_SESSION` and `REX_BLOCK`.
It fires on `startup`, `resume`, `clear` (`/new` and `/clear`), `compact`, and `fork`. So the binding follows every thread change.

`roar hook codex` reads `session_id` from the hook input and writes `codex/<block>.json` in the store:

| Field | Value |
| --- | --- |
| `thread` | `session_id`, which is the thread ID |
| `codex_home` | `$CODEX_HOME`, else `~/.codex` |
| `pid` | The pane's foreground PID from Rex, which is the TUI |
| `at` | Write time |

- The hook prints nothing. Codex adds `SessionStart` output to the model's context, and every token there costs on each turn.
- Outside a Rex pane, or on any error, it does nothing and exits 0. A hook must never block Codex.

A binding is live while Rex reports the same foreground PID for the block. A pane whose TUI exited, or that runs a new Codex that has not run the hook yet, has no live binding.

### Delivery and receipt

Codex delivery runs in whichever command holds the message lock: `roar send`, `roar enqueue`, or `roar deliver`.

1. Without a live binding, skip to the fallback.
2. Connect to the binding's daemon socket and read the thread. If the socket is missing or the thread is `notLoaded` or `systemError`, skip to the fallback. Nothing was sent.
3. Move the message to `written` with writer `codex:<pid>:<start>`.
4. Send it:

| Thread status | Call | Success means |
| --- | --- | --- |
| `idle` | `turn/start {threadId, input}` | `read`, boundary `turn`, proof `codex_turn_start` |
| `active` | `turn/steer {threadId, input, expectedTurnId}`, with the newest `inProgress` turn from `thread/turns/list` | `read`, boundary `mid_turn`, proof `codex_turn_steer` |

- `active` includes `waitingOnApproval` and `waitingOnUserInput`. The steer joins the turn and the model sees it when the turn goes on.
- A turn can end between the read and the steer. Codex then rejects the steer with `-32600`. Roar reads the thread once more and sends again.
- Any other RPC error means not delivered. The message returns to `queued` with hold reason `codex_rejected` and the error as its reason.
- A disconnect or a 15-second timeout after the write is `unknown`. A live test showed a turn still ran after its client disconnected.
- A message left `written` by a `codex:` writer whose claim is free became `unknown` with reason `interrupted_while_writing`. It is never replayed.

## Fallback through Rex

### Readiness proof

`roar ready` runs this proof, and every fallback attempt runs it too.
All readings are fresh, inside a 2-second window. The first verdict wins.

1. **Refuse, dead pane.** A Rex call returns not found (exit 3), `process.last_exit` is set, or there is no foreground process.
2. **Refuse, wrong program.** The foreground process is not `claude` (by `invoked_path` or `argv0`) or `codex` (by `script` or `path`), or its PID differs from the PID recorded when the message was stored.
3. **Unknown, wait.** There is no `program_status` record, its owner's PID or name differs from the foreground process, or its app is not `claude-code` or `codex`. This covers startup, the folder-trust dialog, and a stale record left by a killed agent.
4. **Status.** `working` means busy, wait. `blocked`, `error`, or any other value means unknown, wait.
5. **Composer, for `idle` or `done` only.** Read the screen with `format` (VT, `extra_cursor`):
   - a hidden cursor means unknown, wait;
   - a cursor row that does not start with `❯` or `›` means unknown, wait;
   - a cursor column other than 3 (1-based) means draft, wait;
   - text after the marker, or on continuation rows, that is not dim (SGR 2) means draft, wait. Dim text is placeholder text.
   - Otherwise the composer is empty.

Roar never reads composer text to decide whose draft it is. It asks only whether any non-placeholder text exists.

The status check comes first because a mid-turn composer looks empty. The dim-text check comes after the cursor check because a draft with its cursor moved home passes the cursor check.

### Typing sequence

1. Take the message lock. Run the proof. Record the foreground PID.
2. Set the message to `submitting`.
3. Paste the rendered message (header and body) with `block.event` `paste`, in pieces of at most 32 KiB. Rex brackets each piece, so newlines stay in the draft. `rex send` writes raw bytes and is not used for the body.
4. Run steps 1 to 4 of the proof again, bound to the recorded PID. The composer now holds our text, so step 5 does not apply.
5. Send `enter` with `rex send-key`.
6. Wait up to 5 seconds for the same agent to report `working`, or any state other than `idle` in a report made after Enter. A fast turn can go from `working` to `done` between two readings. If it does, set `submitted` with proof `rex_submit`.

Each proof outcome has a reason that `roar ready` and `roar status` show:

| Verdict | Reasons |
| --- | --- |
| empty | `empty` |
| wait | `busy`, `modal`, `no_status`, `stale_status`, `foreign_status`, `cursor_hidden`, `no_marker`, `no_cursor`, `draft`, `probe_failed`, `stale_probe` |
| refuse | `dead_pane`, `wrong_program`, `owner_changed` |

The unknown reasons are `paste_failed`, `changed_before_enter`, `enter_failed`, `no_turn_started`, and `interrupted_while_typing`.

`roar deliver --watch` follows `rex events --all` and retries a pane's oldest held message on `program_status_changed`, `process_changed`, or `child_exited`. It also sweeps every held message every 30 seconds, for events it missed. It delivers one message per pane per pass, because a delivered message starts a turn and the next would only be held as busy.

If anything fails or changes after step 3, stop without Enter and set `unknown`. The pasted text may now sit in the composer as a draft. Roar does not try to clear it, because it cannot prove that the draft is its own.

Rex has no compare-and-write. A short gap always exists between the last probe and the write.

## Across hosts

The recipient's host owns the message. The sender's host keeps only a `sent/` record.

`roar send` to a pane on another host runs `roar enqueue` there in a detached Rex block.
A detached block belongs to a session but sits in no window, so the operator's app shows nothing.

```text
rex -S <endpoint> api call session.new_block
  session_id: <the recipient host's roar-relay session>
  flavor:     com.superlogical.terminal.shell
  options:    shell "none", login false, exit {on_completion: false},
              size {columns: 4000, rows: 50},
              command [/bin/sh, -c, exec "$HOME/.local/bin/roar" "$@", roar, enqueue, --sha256, <digest>]
```

1. `roar enqueue` puts its terminal in raw mode and prints `ROAR-READY`.
2. The sender waits for that marker on the block's screen, then writes the base64 envelope with the block's `write` method, in chunks of at most 48 KiB, then the end marker `#END#`.
3. `roar enqueue` checks the digest (exit 6 on a mismatch), refuses an envelope for another host (`wrong_host`), stores the message, runs one delivery pass, and prints `ROAR-RESULT <id> <state> <reason>`.
4. The sender polls the block's `process` method until `last_exit` is set. The session ID is required: a detached block is not found without it.
5. The sender reads the screen with `rex capture --trim --unwrap` and closes the block with `block.close`.

| Exit | Meaning |
| --- | --- |
| 0 | Stored, or a duplicate of a stored message |
| 2 | Refused; the reason is the last `roar:` line on the screen |
| 6 | The envelope arrived damaged; the sender retries |
| 126, 127 | Roar is not installed there: `no_roar_on_host` |

Rules:

- `login false` is required. Otherwise Rex runs the command under `/usr/bin/login`, which reports exit 0 whatever the command returned.
- Rex refuses any single API call over 64 KiB. So the envelope cannot travel as one argument; it streams through the terminal in chunks.
- Raw mode is required. Canonical terminal input limits line length. The ready marker exists because input written before raw mode would pass through canonical mode.
- The remote command uses an absolute path, because a detached block's PATH lacks `~/.local/bin`. The default is `$HOME/.local/bin/roar`; the sender's `ROAR_REMOTE_BIN` overrides it.
- The block is 4000 columns wide, and the screen is read unwrapped, so result lines never break.
- Each host keeps one Rex session for these blocks, labelled `roar-relay`. It is not `roar`, because a project named roar already has a session by that label. Roar creates it on first use.
- Rex authenticates the caller as a Tailscale principal, so Roar adds no network listener and no host key.
- A lost answer is an unknown outcome. `roar send` retries twice with the same envelope, and the receiver dedupes by ID.
- `roar status` for a message held on another host runs `roar status --porcelain <id>` there the same way. Each line reads `ROAR-STATUS <id> <state> <detail> <proof> <boundary>`, with `-` for an empty field.
- A reply goes back through the endpoint stored in the original envelope's `from` address.

`roar install <host-label>` copies the running binary to `$HOME/.local/bin/roar` on a host with the same operating system and architecture. It streams base64 to `base64 -d` through the terminal in canonical mode, with echo off, in 76-character lines.

Fallback typing always runs on the recipient's host, against its local Rex server.

## Worked examples

**Claude to idle Codex, same host.**
The sender runs `roar send other-session "Tests pass on main."`.
`roar send` resolves the label to one block and stores the message as `queued`.
The recipient pane's binding names its thread, and Rex reports the same TUI PID, so the binding is live.
`roar send` connects to that Codex home's daemon, reads the thread as `idle`, and calls `turn/start`. The call succeeds.
The message becomes `read` with proof `codex_turn_start`, and `roar send` prints `<id> read`.

**Request to a busy Claude.**
The Claude adapter sees a new `request`. Rex reports the agent `working`, so the adapter holds it.
When Rex emits `program_status_changed` to `done`, the adapter writes the notification.
The transcript shows a `user` record with the message ID. The message becomes `read`, boundary `turn`.

**Fallback into a pane with a draft.**
The recipient's Claude started without the channel flag, so no adapter lock is held.
`roar send` runs the proof: status `idle`, the marker row, the cursor at column 19. The verdict is draft, wait. It exits 0 with the message held.
`roar deliver --watch` runs in a pane on that host. The operator submits the draft. Status goes `working`, then `done`.
On that event the watcher's proof finds an empty composer. It pastes, probes again, presses Enter, and sees `working`. The message becomes `submitted`.

**Native attempt with an unknown outcome.**
`roar send` writes `turn/start` to the Codex daemon, then the connection drops before the result.
The message becomes `unknown`. No fallback runs, even though the pane later looks empty.
`roar status` lists it. The operator checks the conversation and runs `roar resolve <id> --read` or `--requeue`.

**Composing commands.**
`roar ready other-session && roar send other-session "Ready for review."` sends only to an agent at rest.
`roar inbox --json | jq -r '.[].id' | xargs -n1 roar read --mark` reads and marks every waiting message.

## Acceptance criteria

These drive the tests. Each one is covered; the test names are listed after the criteria.

1. A native send to an idle Claude reaches `read` with boundary `turn`. One sent while Claude is mid-tool reaches `read` with boundary `mid_turn`.
2. A meta value that is not a string is refused before writing.
3. `roar mcp` declares no channel and takes no adapter lock when its parent lacks the exact flag.
4. After `/clear`, Claude receipt still matches in the new transcript.
5. A native send to an idle plain `codex` reaches `read` through `turn/start`. One sent while busy uses `turn/steer` with the newest in-progress turn. A steer rejected because the turn ended is sent once more.
6. After `/new` in Codex, a message goes to the new thread, never the old one.
7. A Codex disconnect after the write leaves the message `unknown`, and no fallback runs. A binding whose PID is not the pane's foreground PID, or whose thread is not loaded, sends nothing and falls back.
8. `roar ready` returns the right verdict for each state: empty, draft, draft with cursor home, working, permission prompt, trust dialog, shell, killed agent with a stale status record, exited pane, closed block.
9. Fallback into an empty Claude or Codex composer reaches `submitted`. A changed proof between paste and Enter leaves `unknown` and sends no Enter.
10. Killing any command or adapter at each state change recovers to the documented state with no duplicate delivery.
11. Two deliverers racing on one message deliver it once.
12. A cross-host send dedupes on retry with the same key, and opens no window on either host.
13. Attachments arrive with their message, on the same host or across hosts, and an oversized set is refused.
14. Every rendered message carries one header line, and a sender cannot forge a second header. `roar send` prints the delivery outcome, waiting through a native adapter's delivery.
15. `roar setup` installs the skill in every config root, rewrites a stale Roar-written copy, and leaves an operator's symlink or unmarked file untouched. It registers the MCP server and appends the hook group once each, keeps existing hook groups in order, and goes on past a failed root.
19. `roar hook codex` writes the pane's binding and prints nothing. Outside a Rex pane it writes nothing.
17. A turn on a Codex side thread does not mark the bound thread busy. A delivered message shows no earlier retry error.
18. A bare window or pane label from any session resolves to its pane, and `session/window` and `window@session` resolve too. A label used in two sessions is refused as ambiguous.
20. With no matching label, `codex@session` and `claude@session` name the pane running that agent, and `left@session`, `right@session`, `top@session` and the other position hints name panes by their place in the tab. A label wins over a hint. A hint that names several panes follows the usual rule: the only agent pane wins, else it is refused as ambiguous.
21. A `host-label:` prefix that is this machine's own name, in any case, resolves locally.
22. `peer` names the other pane in the sender's window, for `roar send` and `roar ready`. Among several, the only agent pane wins; a pane alone in its window has no peer.
16. A real Claude and a real Codex that receive a request answer it with `roar send --reply`.

| Criteria | Tests |
| --- | --- |
| 1, 4 | `TestLiveClaudeChannel`; `TestDeliverAcceptReceive`, `TestMidTurnReceipt` |
| 2, 3 | `TestMetaValidation`, `TestGuardRefusesWithoutFlag` |
| 5, 6, 7 | `TestLiveCodexChannel`; `TestIdleThreadStartsTurn`, `TestBusyThreadIsSteered`, `TestStaleSteerIsSentAgain`, `TestDisconnectAfterWriteIsUnknown`, `TestRejectedCallHolds`, `TestStaleBindingFallsBack`, `TestUnloadedThreadFallsBack` |
| 8 | `TestComposerStates`, `TestProcessGates`, `TestStatusGates`, `TestScreenGates`, `TestReadyExitCodes` |
| 9 | `TestSubmitsIntoEmptyComposer`, `TestChangeBetweenPasteAndEnterLeavesUnknown`, `TestLiveClaudeFallback`, `TestLiveCodexFallback` |
| 10 | `TestCrashedSubmitterSettlesToUnknown`, `TestRecoveryAfterWriterChange`, `TestCrashedCodexWriterSettlesToUnknown` |
| 11 | `TestRacingProcessesClaimOnce`, `TestRacingDeliverersSubmitOnce` |
| 12 | `TestEnqueueStoresAndDedupes`, `TestRunOpensNoWindow`, plus the live cross-host run in Evidence |
| 13 | `TestSendWithAttachments`, `TestEnqueueCarriesAttachments`, `TestPutWithFilesRejectsMismatch` |
| 14 | `TestHeaderCannotBeForged`, `TestSendInboxReadStatus`, `TestSettleWaitsForTheAdapter` |
| 15 | `TestInstallWritesSkill`, `TestInstallRewritesStaleCopy`, `TestInstallLeavesOperatorCopy`, `TestFindRoots`, `TestSetupCodexHome`, `TestSetupClaudeRoot`, `TestSetupGoesOnPastAFailedRoot` |
| 19 | `TestHookCodexBindsPane`, `TestHookCodexOutsidePane` |
| 16 | Live run in Evidence (Codex shown; Claude awaits an operator-accepted channel prompt) |
| 5, 6 live | `TestLiveCodexChannel` needs a Codex home with a trusted Roar hook. Idle and busy delivery were shown by hand (Evidence); `/new` awaits the trusted hook. |
| 18 | `TestResolveWindowAndPaneLabels`, plus the live run in Evidence |
| 20 | `TestResolveAgentAndPositionHints`, `TestPositions` |
| 21 | `TestOwnHostPrefixIsLocal`, plus the live run in Evidence |
| 22 | `TestPeer`, `TestSendToPeer`, plus the live run in Evidence |
| 17 | `TestDeliveryClearsEarlierReason` (the side-thread case no longer applies: delivery reads the bound thread's own status) |

## Open questions

| Question | Recommendation |
| --- | --- |
| Where Codex sends an approval request for an injected turn | The TUI shows it, as for any turn on its thread. Not yet seen live: the live turns needed no approval. |
| Whether Codex keeps hook trust after setup reformats `hooks.json` | Setup keeps every group and key in place but re-indents the file. Check `/hooks` after the first real run. |
| What Claude does with an enqueued message it never absorbs (Esc, turn error) | Leave it `accepted` and show its age |
| Program status kinds `question` and `auth` were not seen live | The proof treats every `blocked` as wait, which is safe |
| Attachments count toward no host-wide limit | Only the 16 MiB per-message cap applies. Add a host cap if large unread attachments pile up. |

## Evidence

Live probes on 2026-10-27 used Claude Code 2.1.311, Codex CLI 0.164.0, and two Rex servers (0.0.1793047680 and 0.0.1792787366). Probes on 2026-10-28 used Claude Code 2.1.312 and Codex CLI 0.164.0 with daemon 0.165.0. Rows that name `roar claude` or `roar codex` describe the former wrappers.

| Finding | How it was shown |
| --- | --- |
| A channel server named `roar` works with the equals-form flag | Live: message delivered and answered |
| Mid-turn channel messages arrive as untrusted information | Live: `absorbed_mid_turn` record and system-reminder wrapper |
| A non-string meta value drops the notification silently | Live: emitted, no transcript record |
| The sessions file follows `/clear`; the child's environment does not | Live |
| The MCP child and the Codex TUI inherit `REX_SESSION` and `REX_BLOCK` | Live |
| A second Codex client's `turn/start` renders in the TUI | Live |
| A `turn/start` to the pre-`/new` thread succeeds but is invisible | Live |
| A Codex turn runs after the client disconnects | Live |
| Claude and Codex emit OSC 7501 status natively | Live: `program_status` records with owner PID, locally and remotely |
| A draft leaves status `idle`; a mid-turn composer looks empty | Live |
| A draft with the cursor at home passes a cursor-only check | Live |
| `rex send` returns 0 on an exited keep-open pane | Live |
| `rex send` never brackets; `block.event` paste does | Live: raw bytes read in a raw-mode pane |
| Rex IDs resolve only on their own server | Live: exit 3 across servers |
| Cross-host inspect, send, events, and detached blocks work both ways | Live: 120 to 300 ms per call |
| A detached block runs a command on a remote host with no new window and reports its exit code | Live: exit 5 read back in 0.26 s; window count unchanged |
| Without `login false`, a detached block reports exit 0 for a command that failed | Live |
| Rex refuses an API call over 64 KiB | Live: `control message too large ... limit is 65536` |
| A raw-mode block receives a streamed payload intact | Live: 1 MiB locally in 0.55 s; 1 MiB remote in 4.1 s and 4 MiB in 16.8 s, digest verified. Each 48 KiB chunk was one CLI call. |
| Rex has no durable application inbox | `help.all` and `rex actions` list none |
| Fallback delivers into real Claude and Codex, and a later draft holds the next message | Live test: both agents answered the typed message |
| `roar claude` delivers natively: idle as `turn`, during a tool call as `mid_turn`, a request held while working, receipt after `/clear` | Live test against Claude Code 2.1.311 |
| `roar codex` delivers natively: idle as `turn/start`, while working as `turn/steer`, after `/new` into the thread the TUI shows | Live test against Codex 0.164.0 |
| Cross-host send, remote status, a reply back, and fallback into real Claude on the other host | Live: send 1.9 s (held as `wrong_program` in a shell pane), status 0.6 s, the reply landed in the sender's inbox, fallback `submitted` in 1.7 s and Claude answered |
| `roar install` copies a 9.9 MB binary across hosts | Live: 49 s |
| A detached block's PATH lacks `~/.local/bin` | Live: it held only the Rex server's directory and the system directories |
| Codex reports `idle`, then works briefly during startup | Live: the proof held a message as `busy` 1 s after the first `idle` |
| Rex runs pane commands in its server's login environment, not the caller's | Live: agents started without the caller's `CLAUDE_CONFIG_DIR` or `CODEX_HOME` |
| macOS may report either path of a hard-linked binary | A test agent hard-linked as `claude` and `codex` intermittently failed the owner-name check |
| Codex loads the roar skill for a bare `[roar ...]` header and answers with `roar send --reply` | Live, with no hint in the header: a `request` reached `read` (`codex_turn_start`). Codex read the 1,156-byte skill, replied in one command, and took the printed `queued wrong_program` without a `roar status` call. |
| Codex starts turns on a side thread to name the bound thread | Live, `ROAR_CODEX_TRACE`: `turn/started` and `turn/completed` on a second thread after the first answer. Before the fix the next message got `-32600 no active turn to steer`; after it, `read` on the first attempt. |
| Agents address panes by the Rex tab name | Live: Codex ran `roar send --request build ...` for a window labelled `build` in session `hello-claude` and got `no_such_target`. After the fix, `roar send build` reached `read` in that Claude. |
| An extra `roar status` call costs a whole model call | Live: Claude sent with `roar send`, then ran `roar status`; the second call re-read about 97k tokens of cached context |
| A launch installs the skill in the pane's config root, not the caller's | Live: the Rex pane had no `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, so the skills landed in `~/.claude/skills/roar` and `~/.codex/skills/roar`, the roots those agents read |
| A plain `codex` TUI is a client of the shared daemon, and a second client's `turn/start` renders in it | Live: `thread/loaded/list` on `~/.codex-personal/app-server-control/app-server-control.sock` listed the pane's thread. `turn/start` showed the prompt in the pane, and Codex answered `probe ok`. `thread/status/changed` reported `active`, then `idle`. |
| A second client cannot `thread/resume` a thread the TUI holds | Live: `-32600 no rollout found` for a new thread with no turns. Delivery does not need it. |
| Claude loads a channel only from the launch flag | Claude Code 2.1.312 skips a channel server with `server <name> not in --channels list for this session`. `channelsEnabled` and `allowedChannelPlugins` are managed policy, not a way to turn a channel on. |
| Codex `SessionStart` fires on `startup`, `resume`, `clear`, `compact`, and `fork` | The hook input schema in the Codex 0.165.0 binary |
| `roar send` delivers to a plain `codex` through its daemon | Live: with the binding `roar hook codex` wrote for the pane, `roar send` printed `read` (`codex_turn_start`) and Codex answered `DAEMON OK`. A message sent 3 s into a `sleep 8` turn reached `read` as `codex_turn_steer`, `mid_turn`, and Codex took it into that turn. |
| `roar setup` changes only what differs, and keeps an operator's hooks | Run twice against a copy of `~/.codex-personal/hooks.json` under a throwaway `HOME`: the second run printed `ok` for every root. The only change to the file was Roar's group, appended last. `claude mcp add-json` wrote the server to `~/.claude.json` for `~/.claude`, and to `<root>/.claude.json` for the others. |
| Agent and position hints name panes in a real layout | Live, session `roar` with Claude left and Codex right, both panes labelled `shell`: `codex@roar` and `right@roar` named the Codex pane, `claude@roar` and `left@roar` the Claude pane, and `top@roar` was refused as ambiguous. `rex window ls --json` gives each tiled pane's `rect` as fractions of the window. |
| This machine's own name works as a host prefix | Live on Stiletto (host name `Stiletto.local`, Rex label empty): `roar ready stiletto:claude@roar` gave `wait: busy` for the Claude pane and `STILETTO:codex@roar` gave `empty` for the Codex pane. `Baroni:nonexistent-xyz` still resolved on Baroni, and `nohost:codex@roar` was still refused. |
| A new hook needs operator trust; a bypass is refused | `--dangerously-bypass-hook-trust` exists, but Roar never uses it, and an agent's attempt to was refused by its own safety check |

## Related documents

- Rex CLI guide: `rex __internal skill`.
- Rex program status protocol: the `com.superlogical.terminal.program_status` method, `rex api describe`.
- Rex server API: `rex api ls`.
