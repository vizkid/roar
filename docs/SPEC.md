# Ping specification

Owner: Viz. Updated: 2026-10-27. Status: draft, not ratified.

## Problem and solution

Agents in different terminal panes need to send each other messages.
Typing into another pane is unsafe: it can land in a draft, a shell, or a permission prompt.
Queued terminal bytes also prove nothing about whether the agent read them.

Ping is a small set of commands that routes messages between Rex panes.
It delivers through each agent's native channel when one is connected.
When no native channel exists, it types through Rex, but only after positive proof that the pane holds an agent with an empty composer.
Otherwise it holds the message.

> **Core insight: a message is delivered only when the provider says so.** Every other signal (a file written, a socket write, queued bytes, a visible pane) only moves a message closer. Ping never sends a message a second time by another path while an earlier attempt has an unknown outcome.

## Scope

| In scope | Out of scope |
| --- | --- |
| Messages between Rex panes, on one host or across Rex hosts | A separate agent identity or address registry |
| Native delivery adapters for Claude Code and Codex | Task coordination and issue launching (deferred) |
| Terminal-input fallback through Rex, with readiness proof | Credentials and accounts (separate tools) |
| A durable per-host message store | Roles, delegation, broadcast |
| Launch helpers that set up native adapters | Service installation; the operator decides where long-running commands run |
| Reading agent turn state from Rex program status | Deriving agent state from terminal polling, screen scraping, hooks, or transcripts |

Rex supplies identity. A pane is a Rex block on a Rex server. Ping adds no names of its own.

## Design principles

- **Commands, not a service.** Every command works on its own. There is no Ping daemon.
- **The store is the interface.** Commands share state through files in one directory per host. File locks serialize changes.
- **Long-lived only where the provider demands it.** The Claude adapter lives as long as Claude does. The Codex proxy lives as long as Codex does. `ping deliver --watch` is optional.
- **Plain output.** Text by default, `--json` for scripts. Exit codes carry the outcome.

Implementation language: Go.

## Commands

| Command | Does | Exit codes |
| --- | --- | --- |
| `ping send <target> [text]` | Stores a message for the target, then runs one delivery pass for it. Reads the body from stdin when no text is given. Prints the message ID. `--request` sets `kind: request`; `--reply <id>` answers a message in the caller's inbox; `--key <k>` makes a retried send store once. | 0 stored, 2 refused |
| `ping inbox [--all]` | Lists unread messages addressed to the caller's own pane. `--all` includes read and expired ones. A read peeks. | 0 |
| `ping read <id> [--mark]` | Prints one message. `--mark` marks it read; only the recipient pane may do that. | 0, 2 |
| `ping status [id...]` | Shows the state of messages the caller sent, or of the given IDs. | 0 |
| `ping withdraw <id>` | Expires the caller's own unread message. | 0, 2 |
| `ping resolve <id> --read\|--requeue` | Settles an `unknown` message after the operator checked the conversation. | 0, 2 |
| `ping ready <target>` | Prints the fallback readiness verdict for a pane. | 0 empty, 1 wait, 2 refuse |
| `ping deliver [target...] [--watch]` | Runs fallback delivery for queued messages whose recipients have no native adapter. `--watch` follows `rex events` and retries on status changes. | 0, 2 |
| `ping enqueue --sha256 <digest>` | Plumbing for cross-host sends: reads a forwarded envelope from stdin, stores it, then runs one delivery pass. | 0 stored or duplicate, 2 refused, 6 digest mismatch |
| `ping claude [args...]` | Runs Claude Code with the Ping channel loaded. | Claude's |
| `ping mcp` | Claude adapter. Claude starts it; operators never run it. | |
| `ping codex [args...]` | Runs Codex behind the Ping proxy. | Codex's |

Exit code 2 always comes with one line on stderr that names the reason, for example `ambiguous_target`, `no_ping_on_host`, or `limit_exceeded`.

## Addressing

A recipient is one Rex block: `(server, session_id, block_id)`.

- Rex IDs resolve only on their own server. A block ID sent to another server returns "not found".
- Store the server as its `host_id` plus endpoint URL from `rex whoami --json`. Host labels are local nicknames and differ between hosts.
- Store the server's `instance_id` too. A changed `instance_id` means the server restarted, and stored block IDs are invalid.
- A target is a full block ID, or `[host-label:]session-label[/pane]`. A pane is a pane label or the last six or more characters of a block ID. `ping send` resolves the target once and stores IDs.
- Without a pane, a session with one pane resolves to it. A session with several panes resolves to its only agent pane, by foreground process. Otherwise the send is refused with `ambiguous_target` and the candidates are listed.

### Sender identity

`ping send` reads `REX_SESSION` and `REX_BLOCK` from its own environment and `rex whoami --json` for the host.
Outside a Rex pane it refuses with `not_in_rex_pane`.

Every pane runs as the same user. This identity prevents mistakes. It is not authentication.

## The envelope

```json
{
  "id": "pm-01JB...",
  "from": {"host_id": "...", "session": "session:...", "block": "block:..."},
  "to":   {"host_id": "...", "session": "session:...", "block": "block:..."},
  "kind": "inform",
  "text": "...",
  "reply_to": "pm-...",
  "idempotency_key": "...",
  "created_at": "2026-10-27T12:00:00Z",
  "forwarded": false
}
```

| Field | Rule |
| --- | --- |
| `id` | ULID with a `pm-` prefix. With an `idempotency_key`, the ID derives from `sha256(host_id, from.block, key)`, so a retried send gets the same ID. |
| `kind` | `inform` or `request`. Unknown values become `inform`. |
| `reply_to` | Optional. `ping send --reply <id>` reads the original envelope from the caller's own inbox and addresses its sender. |
| `forwarded` | True when the envelope arrived from another host. A forwarded envelope is never forwarded again. |

The agent sees one header line, then the body: `[ping <id> from <session-label>/<block-short> <kind> ↩<reply_to>]`.
Ping strips control characters and newlines from every header field.

## The store

One directory per host: `$PING_HOME`, default `$XDG_STATE_HOME/ping` (`~/.local/state/ping`). Mode 0700.

```text
$PING_HOME/
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

Retention: unread messages are never pruned. Read and expired messages are kept 7 days, so a late reply can still find its original. Pruning runs during `ping send` and `ping enqueue`.

Limits: 256 KiB per message, 2 MiB of unread messages per recipient, 16 MiB per host. A send over a limit is refused before anything is stored.

## Agent activity from Rex

Rex tracks each agent's turn state through its program status protocol (OSC 7501).
Claude Code and Codex both report it natively. No hook setup is needed.
Ping reads it with the block's `program_status` method and watches `program_status_changed` in `rex events`.

| Status | Meaning for Ping |
| --- | --- |
| `working` | Mid-turn |
| `done` | The turn finished. It stays `done` until the next input. Ping treats it like `idle`. |
| `idle` | At rest |
| `blocked` | Waiting on a modal, such as a permission prompt (`kind` says which) |
| No record | Starting up, in a dialog before the agent reports, or not an agent |

A record counts only when its owner's PID and name match the block's foreground process. The system reuses PIDs, and a killed agent leaves a stale record behind.

Rex program status is Ping's only source of agent turn state.
Ping reads Claude transcripts for one purpose, message receipt, and never for turn state.

Ping uses the status in four places:

1. The Claude adapter holds a `request` until the status is `idle` or `done`.
2. Adapters and `ping deliver --watch` retry held messages on `program_status_changed`, with no polling loop.
3. It is the first gate of the fallback proof.
4. It confirms a fallback submit: `working` after Enter.

Status alone cannot prove an empty composer. A typed draft leaves the status `idle`, which was verified live. So the fallback still checks the composer row.

## Delivery

### Choosing a path

| Recipient state | Path |
| --- | --- |
| A native adapter holds `adapters/<block>.lock` | Native. Only the adapter delivers. |
| No native adapter, and the readiness proof says empty | Fallback, by whichever `ping send`, `ping enqueue`, or `ping deliver` holds the message lock |
| No native adapter, and the proof says busy, draft, modal, or unknown | Hold. `ping deliver --watch` or the next send retries. |
| The pane runs something that is not an agent, such as a shell | Hold with `wrong_program`. Ping never types into it; an agent may start there later, and a person can still read the message with `ping inbox`. |
| The block is gone or its program exited | Expire with `dead_pane` |
| An earlier attempt for this message has an unknown outcome | Hold. Nothing tries again until `ping resolve`. |

A pane can gain or lose its native adapter. Ping decides per attempt, never per message.
Without `ping deliver --watch` running on the recipient's host, a held fallback message waits for the next send to that pane or a manual `ping deliver`.

### States

| State | Entered when | Next |
| --- | --- | --- |
| `queued` | The message directory was renamed into `box/` | `written`, `submitting`, `expired` |
| `written` | A native adapter handed the message to the provider | `accepted`, `read`, `queued`, `unknown` |
| `accepted` | Claude recorded the channel message as enqueued | `read`, `queued` (removed without receipt) |
| `read` | Provider receipt proof arrived, or the recipient ran `ping read --mark` | Final |
| `submitting` | The fallback began typing | `submitted`, `unknown` |
| `submitted` | Enter was sent and Rex reported the agent `working` | Final |
| `unknown` | A provider call or a typing sequence ended without a result | `read` or `queued`, by `ping resolve` only |
| `expired` | The recipient block ended or changed program, or the sender withdrew the message | Final |

`held` and `blocked` are views, not states.
`held` is a `queued` message with a hold reason in `state.json`.
`blocked` is a `written` message older than 30 seconds whose adapter is still live.

`read` and `submitted` differ on purpose. `read` comes from the provider. `submitted` only proves that our Enter started a turn.

### Receipt detail

Each `read` records its proof and its boundary.

| Proof | Boundary |
| --- | --- |
| `claude_transcript` | `turn` (the message started a turn) or `mid_turn` (Claude absorbed it inside a running turn) |
| `codex_turn_start` | `turn` |
| `codex_turn_steer` | `mid_turn` |
| `marked` | None. The recipient ran `ping read --mark`. |

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

## Claude adapter

### Launch

`ping claude [args...]` runs the real Claude binary with:

- `--mcp-config` naming a server `ping` that runs `ping mcp`;
- `--dangerously-load-development-channels=server:ping` in equals form, so a list-valued flag cannot consume the prompt.

Claude starts MCP servers only after the operator accepts the development-channel consent dialog.
Ping does not answer that dialog. The operator accepts it once per launch.

Claude's startup banner can say `server:ping · no MCP server configured with that name` while delivery works. Ping never reads that banner.

### Binding

`ping mcp` inherits `REX_SESSION` and `REX_BLOCK` from the pane.
Its parent is the Claude process. Before declaring the channel, it reads the parent's argv and requires the exact `server:ping` channel flag and no print mode.
Without that proof it declares no channel, takes no adapter lock, and logs `channel_flag_missing`. The pane then falls back to terminal delivery.

The check runs every time `ping mcp` starts. A provider self-update can restart Claude without the flag.

### Notification

```json
{"jsonrpc":"2.0","method":"notifications/claude/channel",
 "params":{"content":"<header and body>","meta":{"id":"pm-...","sender":"<from>"}}}
```

Claude renders this as `<channel source="ping" id="..." sender="...">` around the content.
Ping validates meta before writing, because Claude drops a bad notification silently:

- every value is a string;
- every key is an identifier;
- no key is `source`, which would duplicate the source attribute.

### Receipt from the transcript

Find the transcript from `$CLAUDE_CONFIG_DIR/sessions/<claude pid>.json`, where the PID is `ping mcp`'s parent.
That file holds the current `sessionId` and `cwd`. It follows `/clear`. The child's `CLAUDE_CODE_SESSION_ID` environment variable does not.

| Transcript record | Meaning |
| --- | --- |
| `queue-operation` `enqueue` with the envelope | `accepted` |
| `user`, `isMeta`, `origin.kind=channel`, `origin.server=ping` | `read`, boundary `turn` |
| `queue-operation` `remove` with `reason=absorbed_mid_turn`, then `attachment` `queued_command` with the same origin | `read`, boundary `mid_turn` |
| `remove` with no receipt after a 2-second grace and one re-scan | back to `queued` |

Match the message ID by parsing the opening `<channel>` tag. Reject a tag with duplicate attributes.
Read only records written after the message was written, and only in this Claude process's own transcript.

These records are undocumented provider formats. If the sessions file or the records are missing, the message stays `accepted` or `written` and `ping status` shows why. Ping never guesses receipt.

### Replay

Replay an `accepted` message only when the `ping mcp` process that wrote it has changed. Same writer means Claude still holds it.
Each adapter records its writer identity (`mcp:<pid>:<start time>`) and the write time on every message it writes. It records the write before it writes, so a crash never leaves a written message looking queued.
Only `queued` messages are written, so one adapter never writes the same ID twice. A second write happens only after a removal without receipt, or after the writer changed. Both are intended replays.

The adapter's MCP `instructions` tell the model how to read the header and answer with `ping send --reply <id>`.

## Codex adapter

### Launch

`ping codex [args...]`:

1. Creates a private directory with mode 0700 under a short base path, because macOS limits Unix socket paths to 104 bytes.
2. Starts `codex app-server --listen unix://<dir>/server.sock`.
3. Starts the proxy on `<dir>/proxy.sock` and takes `adapters/<block>.lock`.
4. Runs `codex --remote unix://<dir>/proxy.sock [args...]` in the pane, and stops the App Server and proxy when it exits.

Ping never listens on a TCP port for Codex. A loopback WebSocket without auth lets any local process drive the agent.

### Why a proxy

A second App Server client can start turns that render in the TUI. But it cannot tell which thread the TUI shows.
After `/new` the old thread stays loaded. A `turn/start` to the old thread succeeds and the model answers, yet the TUI shows nothing.
The proxy sees the TUI's own `thread/start`, `thread/resume`, and `thread/fork` responses, so it always knows the current thread.

### Binding

- The bound connection is the first `codex-tui` connection whose own durable, top-level thread call succeeds.
- Later thread calls on that connection rebind the thread. Ephemeral threads and sub-agent threads never bind.
- While any TUI thread call is outstanding, delivery waits.
- The active turn comes from `turn/started` and `turn/completed`, from an `inProgress` turn in a thread response, and from forwarded `turn/start` results, which arrive first.
- Each delivery binds to the connection and thread captured at bind time. If either changed, the delivery is refused and the message stays `queued`.

### Delivery and receipt

| Thread state | Call | Success means |
| --- | --- | --- |
| Idle | `turn/start {threadId, input}` | `read`, boundary `turn` |
| Busy | `turn/steer {threadId, input, expectedTurnId}` | `read`, boundary `mid_turn` |

- The proxy injects calls on the TUI's own upstream connection with a reserved ID prefix. It hides those responses from the TUI, and refuses a TUI frame that uses the prefix.
- An RPC error means not delivered. The message returns to `queued` with hold reason `codex_rejected`. A stale steer returns `-32600`; the proxy then forgets that turn, so the next attempt starts a turn.
- With no bound thread, or while a TUI thread call is outstanding, the message returns to `queued` with hold reason `codex_not_ready`. Nothing was sent.
- A disconnect or a 15-second timeout after the write is `unknown`. A live test showed the turn still ran after the client disconnected.
- The adapter delivers queued messages oldest first. A message that arrives while a turn runs steers that turn.
- A message left `written` by an earlier `ping codex` process becomes `unknown` with reason `adapter_restarted`, never replayed.

### Process lifetime

The App Server runs in its own process group, so Ctrl-C in the pane reaches only the TUI. `ping codex` ignores SIGINT and SIGQUIT while the TUI runs, like a shell does.
A pane hangup or kill does not reach that group either. So `ping codex` stops the App Server on SIGHUP and SIGTERM.
Nothing can clean up after SIGKILL. Each launch writes `<dir>/owner` with its own PID and the server's process group. The next `ping codex` start stops servers whose owner is dead, and removes their directories. It signals a group only when the process command still names that directory's socket, because process IDs are reused.

## Fallback through Rex

### Readiness proof

`ping ready` runs this proof, and every fallback attempt runs it too.
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

Ping never reads composer text to decide whose draft it is. It asks only whether any non-placeholder text exists.

The status check comes first because a mid-turn composer looks empty. The dim-text check comes after the cursor check because a draft with its cursor moved home passes the cursor check.

### Typing sequence

1. Take the message lock. Run the proof. Record the foreground PID.
2. Set the message to `submitting`.
3. Paste the rendered message (header and body) with `block.event` `paste`, in pieces of at most 32 KiB. Rex brackets each piece, so newlines stay in the draft. `rex send` writes raw bytes and is not used for the body.
4. Run steps 1 to 4 of the proof again, bound to the recorded PID. The composer now holds our text, so step 5 does not apply.
5. Send `enter` with `rex send-key`.
6. Wait up to 5 seconds for the same agent to report `working`, or any state other than `idle` in a report made after Enter. A fast turn can go from `working` to `done` between two readings. If it does, set `submitted` with proof `rex_submit`.

Each proof outcome has a reason that `ping ready` and `ping status` show:

| Verdict | Reasons |
| --- | --- |
| empty | `empty` |
| wait | `busy`, `modal`, `no_status`, `stale_status`, `foreign_status`, `cursor_hidden`, `no_marker`, `no_cursor`, `draft`, `probe_failed`, `stale_probe` |
| refuse | `dead_pane`, `wrong_program`, `owner_changed` |

The unknown reasons are `paste_failed`, `changed_before_enter`, `enter_failed`, `no_turn_started`, and `interrupted_while_typing`.

`ping deliver --watch` follows `rex events --all` and retries a pane's oldest held message on `program_status_changed`, `process_changed`, or `child_exited`. It also sweeps every held message every 30 seconds, for events it missed. It delivers one message per pane per pass, because a delivered message starts a turn and the next would only be held as busy.

If anything fails or changes after step 3, stop without Enter and set `unknown`. The pasted text may now sit in the composer as a draft. Ping does not try to clear it, because it cannot prove that the draft is its own.

Rex has no compare-and-write. A short gap always exists between the last probe and the write.

## Across hosts

The recipient's host owns the message. The sender's host keeps only a `sent/` record.

`ping send` to a pane on another host runs `ping enqueue` there in a detached Rex block.
A detached block belongs to a session but sits in no window, so the operator's app shows nothing.

```text
rex -S <endpoint> api call session.new_block
  session_id: <the recipient host's ping-relay session>
  flavor:     com.superlogical.terminal.shell
  options:    shell "none", login false, exit {on_completion: false},
              size {columns: 4000, rows: 50},
              command [/bin/sh, -c, exec "$HOME/.local/bin/ping" "$@", ping, enqueue, --sha256, <digest>]
```

1. `ping enqueue` puts its terminal in raw mode and prints `PING-READY`.
2. The sender waits for that marker on the block's screen, then writes the base64 envelope with the block's `write` method, in chunks of at most 48 KiB, then the end marker `#END#`.
3. `ping enqueue` checks the digest (exit 6 on a mismatch), refuses an envelope for another host (`wrong_host`), stores the message, runs one delivery pass, and prints `PING-RESULT <id> <state> <reason>`.
4. The sender polls the block's `process` method until `last_exit` is set. The session ID is required: a detached block is not found without it.
5. The sender reads the screen with `rex capture --trim --unwrap` and closes the block with `block.close`.

| Exit | Meaning |
| --- | --- |
| 0 | Stored, or a duplicate of a stored message |
| 2 | Refused; the reason is the last `ping:` line on the screen |
| 6 | The envelope arrived damaged; the sender retries |
| 126, 127 | Ping is not installed there: `no_ping_on_host` |

Rules:

- `login false` is required. Otherwise Rex runs the command under `/usr/bin/login`, which reports exit 0 whatever the command returned.
- Rex refuses any single API call over 64 KiB. So the envelope cannot travel as one argument; it streams through the terminal in chunks.
- Raw mode is required. Canonical terminal input limits line length. The ready marker exists because input written before raw mode would pass through canonical mode.
- The remote command uses an absolute path. In a detached block's PATH, a bare `ping` finds the system's ICMP ping. The default is `$HOME/.local/bin/ping`; the sender's `PING_REMOTE_BIN` overrides it.
- The block is 4000 columns wide, and the screen is read unwrapped, so result lines never break.
- Each host keeps one Rex session for these blocks, labelled `ping-relay`. It is not `ping`, because a project named ping already has a session by that label. Ping creates it on first use.
- Rex authenticates the caller as a Tailscale principal, so Ping adds no network listener and no host key.
- A lost answer is an unknown outcome. `ping send` retries twice with the same envelope, and the receiver dedupes by ID.
- `ping status` for a message held on another host runs `ping status --porcelain <id>` there the same way. Each line reads `PING-STATUS <id> <state> <detail> <proof> <boundary>`, with `-` for an empty field.
- A reply goes back through the endpoint stored in the original envelope's `from` address.

`ping install <host-label>` copies the running binary to `$HOME/.local/bin/ping` on a host with the same operating system and architecture. It streams base64 to `base64 -d` through the terminal in canonical mode, with echo off, in 76-character lines.

Fallback typing always runs on the recipient's host, against its local Rex server.

## Worked examples

**Claude to idle Codex, same host.**
The sender runs `ping send other-session "Tests pass on main."`.
`ping send` resolves the label to one block and stores the message as `queued`.
The recipient's Codex proxy holds the adapter lock, so `ping send` leaves delivery to it and exits 0.
The proxy sees the new directory, finds an idle bound thread, and calls `turn/start`. The call succeeds.
The message becomes `read` with proof `codex_turn_start`.

**Request to a busy Claude.**
The Claude adapter sees a new `request`. Rex reports the agent `working`, so the adapter holds it.
When Rex emits `program_status_changed` to `done`, the adapter writes the notification.
The transcript shows a `user` record with the message ID. The message becomes `read`, boundary `turn`.

**Fallback into a pane with a draft.**
The recipient runs plain Claude, so no adapter lock is held.
`ping send` runs the proof: status `idle`, the marker row, the cursor at column 19. The verdict is draft, wait. It exits 0 with the message held.
`ping deliver --watch` runs in a pane on that host. The operator submits the draft. Status goes `working`, then `done`.
On that event the watcher's proof finds an empty composer. It pastes, probes again, presses Enter, and sees `working`. The message becomes `submitted`.

**Native attempt with an unknown outcome.**
The Codex proxy writes `turn/start`, then the App Server connection drops before the result.
The message becomes `unknown`. No fallback runs, even though the pane later looks empty.
`ping status` lists it. The operator checks the conversation and runs `ping resolve <id> --read` or `--requeue`.

**Composing commands.**
`ping ready other-session && ping send other-session "Ready for review."` sends only to an agent at rest.
`ping inbox --json | jq -r '.[].id' | xargs -n1 ping read --mark` reads and marks every waiting message.

## Acceptance criteria

These drive the tests.

1. A native send to an idle Claude reaches `read` with boundary `turn`. One sent while Claude is mid-tool reaches `read` with boundary `mid_turn`.
2. A meta value that is not a string is refused before writing.
3. `ping mcp` declares no channel and takes no adapter lock when its parent lacks the exact flag.
4. After `/clear`, Claude receipt still matches in the new transcript.
5. A native send to idle Codex reaches `read` through `turn/start`. One sent while busy uses `turn/steer` with the current turn ID.
6. After `/new` in Codex, a message goes to the new thread, never the old one.
7. A Codex disconnect after the write leaves the message `unknown`, and no fallback runs.
8. `ping ready` returns the right verdict for each state: empty, draft, draft with cursor home, working, permission prompt, trust dialog, shell, killed agent with a stale status record, exited pane, closed block.
9. Fallback into an empty Claude or Codex composer reaches `submitted`. A changed proof between paste and Enter leaves `unknown` and sends no Enter.
10. Killing any command or adapter at each state change recovers to the documented state with no duplicate delivery.
11. Two deliverers racing on one message deliver it once.
12. A cross-host send dedupes on retry with the same key, and opens no window on either host.

## Open questions

| Question | Recommendation |
| --- | --- |
| The command name `ping` shadows the system's ICMP `ping` wherever `~/.local/bin` comes first in PATH | Viz's call: keep the name and accept the shadowing, or install the binary under another name. Verified live: after `ping install`, an interactive shell on the remote host resolved `ping` to Ping. |
| Where Codex sends an approval request for an injected turn | Test before calling the Codex adapter done |
| Whether a Codex picker connection sends thread calls that look like a view change | Test; until then only the first bound connection may rebind |
| What Claude does with an enqueued message it never absorbs (Esc, turn error) | Leave it `accepted` and show its age |
| Program status kinds `question` and `auth` were not seen live | The proof treats every `blocked` as wait, which is safe |

## Evidence

Live probes on 2026-10-27 used Claude Code 2.1.311, Codex CLI 0.164.0, and two Rex servers (0.0.1793047680 and 0.0.1792787366).

| Finding | How it was shown |
| --- | --- |
| A channel server named `ping` works with the equals-form flag | Live: message delivered and answered |
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
| `ping claude` delivers natively: idle as `turn`, during a tool call as `mid_turn`, a request held while working, receipt after `/clear` | Live test against Claude Code 2.1.311 |
| `ping codex` delivers natively: idle as `turn/start`, while working as `turn/steer`, after `/new` into the thread the TUI shows | Live test against Codex 0.164.0 |
| Killing the pane left the App Server running until `ping codex` handled SIGHUP | Live: an orphaned `codex app-server` after `rex kill`; none after the fix |
| Cross-host send, remote status, a reply back, and fallback into real Claude on the other host | Live: send 1.9 s (held as `wrong_program` in a shell pane), status 0.6 s, the reply landed in the sender's inbox, fallback `submitted` in 1.7 s and Claude answered |
| `ping install` copies a 9.9 MB binary across hosts | Live: 49 s |
| A detached block's PATH finds the ICMP `ping` first | Live: `/sbin/ping` |
| Codex reports `idle`, then works briefly during startup | Live: the proof held a message as `busy` 1 s after the first `idle` |
| Rex runs pane commands in its server's login environment, not the caller's | Live: agents started without the caller's `CLAUDE_CONFIG_DIR` or `CODEX_HOME` |
| macOS may report either path of a hard-linked binary | A test agent hard-linked as `claude` and `codex` intermittently failed the owner-name check |

## Related documents

- Rex CLI guide: `rex __internal skill`.
- Rex program status protocol: the `com.superlogical.terminal.program_status` method, `rex api describe`.
- Rex server API: `rex api ls`.
