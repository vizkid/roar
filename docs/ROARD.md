# roard: one Roar service per host

Owner: Viz. Updated: 2026-11-02. Status: built on `main` and installed as a service on Stiletto, Baroni and Colonelk on 2026-11-02; all three `roar-relay` sessions are retired; not ratified. When it is installed, these sections replace the parts of [SPEC.md](SPEC.md) that they name.

## Problem and solution

Roar needs two things that a plain command cannot give it.

- **Something that outlives the command.** A held typed message needs a watcher that retries it. Today the watcher runs in a Rex block, because a child of an agent's tool call can be killed with that call.
- **A way to run Roar on another host.** Today `roar send` opens a detached Rex block on the other host, streams the envelope through its terminal, and polls the block until it exits.

Rex blocks must live in a session. So every host carries a session labelled `roar-relay`, and it shows in the Rex sidebar as a project. Creating it windowless removes its tab but not the project.

roard is one long-running Roar process per host. The OS service manager keeps it alive. It connects to the local Rex server as a Rex client labelled `roard` and offers Roar's remote operations as client actions. A sender on another host connects to that host's Rex server as a client too, and offers one action of its own, `roar.result`. Each side calls the other's actions with Rex's `client.perform`, so results arrive as calls, not as answers to polls. The watcher runs inside roard and wakes on events.

> **Core insight: both ends are Rex clients, so every step is a call or an event.** The sender calls roard to hand over a message. roard calls the sender back when the message settles. roard learns about agents from Rex events and about the store from filesystem events. Timers remain only where time itself is the trigger, such as a deadline or a reconnect delay.

## What changes

| Today | With roard |
| --- | --- |
| The watcher runs as `roar deliver --watch --until-idle` in a detached block of `roar-relay` | The watcher runs inside roard |
| A command that holds a message starts a watcher block | roard sees the new record through a filesystem event |
| Remote enqueue, status and panes open a block, wait for `ROAR-READY`, stream input, and poll the block every 100 ms | The sender calls roard's actions on its own control connection. Results come back as `roar.result` calls. |
| A 12 MiB attachment is encoded twice and sent as about 456 writes of 48 KiB | Files are encoded once and sent in parts of about 768 KiB, about 22 calls |
| `roar install` and remote setup use a block in `roar-relay` | Install uses a block in an existing session, else in a temporary windowless session that it destroys. Setup runs inside that bootstrap. |
| Each host keeps a `roar-relay` session | No host keeps a Roar session. roard destroys the old one once it holds no work. |
| "Commands, not a service." Service installation is out of scope. | `roar setup` installs and starts the roard service. |

Everything else stays: the store, the adapters, the readiness proof, the envelope, and the delivery states.

## Design principles, revised

- **Commands first, one service for what outlives them.** Every command still works on its own for local delivery. roard does two jobs: it watches held messages, and it answers other hosts.
- **The store is still the interface.** roard reads and writes the same store with the same file locks. A command never needs roard to deliver to a pane on its own host.
- **Rex is the only transport.** roard opens no listener of its own. It reaches other hosts only through Rex, and Rex decides who may connect.
- **Events wake work; timers only bound it.** A timer may set a deadline, delay a reconnect, or cover a gap that no event reports. Each remaining timer is listed in [Timers](#timers), with its reason.

## How roard runs

| Item | Rule |
| --- | --- |
| Command | `roar daemon`. The binary stays `roar`; `roard` is the service name and the Rex client label. |
| One per host | roard takes an exclusive lock on `roard.lock` in the store. A second roard exits at once with status 0. |
| Rex connection | One WebSocket, `GET /api/control` on the local server, subprotocol `rex.control.v1`, API revision 7. The socket comes from `REX_SERVER`, else Rex's default socket. |
| Hello | `client.kind` `other`, `name` `roard`, `version` the Roar version, `label` `roard`. Then `client.set_label` `roard` and `client.keymap` with the action list and `server_actions` `none`. |
| Events | `session.attach_all` with `event_scope` `all`, sent before roard reads any state. See [The watcher inside roard](#the-watcher-inside-roard). |
| Reconnect | When the connection drops, roard reconnects with backoff (`roard.rex.reconnect.*`; by default 1, 2, 4 seconds and so on, up to 30 seconds, with jitter, reset after 60 seconds of connection). After each reconnect roard declares its label and actions again, subscribes again, and reconciles. A reconnect token keeps the client ID, but roard never assumes Rex replays missed events. |
| New binary | `roar install` and `roar setup` restart the service after they replace the binary. For a manual build, roard watches the binary's directory and restarts itself when a new file is renamed onto the binary's path. No timer checks the binary. |
| Log | `~/.local/state/roar/roard.log`. At 1 MiB it moves to `roard.log.1` and starts a new file. It records each call's action, caller principal, message ID, outcome and duration, never message text. |

### Inside the process

| Part | Job | Rule |
| --- | --- | --- |
| Reader | Reads every message from the Rex connection | Never blocks. It hands requests and events to the parts below. Rex disconnects a client whose unread events exceed 8 MiB. |
| Action handlers | Answer each `client_perform_requested` | Validate the arguments, record the job, reply with `client.reply`. No disk-heavy work, no delivery. |
| Workers | Decode, check digests, write the store, run delivery passes, probe panes | Four workers. Uploads and control jobs share them fairly: no upload may hold more than two workers. |
| Callback queue | Calls `roar.result` on senders | Runs after the job has released every store lock. It never holds up a handler. |

### The service

`roar setup` writes the service file, and starts or restarts roard when the file or the binary changed.

| Host | File | Settings |
| --- | --- | --- |
| macOS | `~/Library/LaunchAgents/roar.roard.plist`, label `roar.roard` | `ProgramArguments` = the absolute Roar path and `daemon`. `RunAtLoad` and `KeepAlive` true. `PATH` holds the directory of the `rex` that setup found. Loaded with `launchctl bootstrap gui/<uid>`, restarted with `launchctl kickstart -k`. |
| Linux | `~/.config/systemd/user/roard.service` | `ExecStart` = the absolute Roar path and `daemon`. `Restart=always`, `RestartSec=2`. `After=` and `Wants=` `rex.service` when that unit exists. Started with `systemctl --user enable --now roard`. |

Colonelk already runs Rex as the user unit `rex.service`, with lingering on, so roard starts at boot there too. A second setup run that changes nothing prints `ok`.

## The sender's connection

`roar send`, `roar status` and `roar panes` for another host open one control connection to that host's Rex server, not to roard directly. They do not use the `rex` CLI for this.

- The client says `kind` `cli`, `name` `roar`, and has no label.
- It lists one action, `roar.result`, in `client.keymap`.
- It uses the endpoint that Rex's host list gives for that host, the same endpoint `rex -S <host>` uses.
- It reads `limits.max_message_bytes` from the welcome and sizes every request below it.
- One connection carries the whole command: subscribe, upload, commit, and every result.

roard calls `roar.result` on the requester's client ID, which Rex puts in each request. So the receiver needs no route back to the sender's host, and the sender needs no roard of its own.

## Actions

roard offers these client actions. Their names start with `roar.`, because Rex reserves names without a dot for its configuration file.

| Action | Arguments | Immediate reply | Later `roar.result` |
| --- | --- | --- | --- |
| `roar.hello` | none | `version`, `host_id`, `protocol` (1), `max_part` | none |
| `roar.subscribe` | `ids` | `subscription` and the current record for each ID, with its `revision` | Each committed change to those records |
| `roar.enqueue` | `envelope` (JSON, under the part limit) and `sha256` | `job` | The stored record, then its changes |
| `roar.upload.begin` | `id`, `envelope`, `files` (name, size, sha256 each) | `upload` and the offset already committed for each file | none |
| `roar.upload.part` | `upload`, `file`, `offset`, `data` (base64 of raw file bytes) | `committed`, the file's committed offset | none |
| `roar.upload.commit` | `upload`, `sha256` | `job` | The stored record, then its changes |
| `roar.status` | `ids` | `job` | One row per ID, the fields of `ROAR-STATUS` |
| `roar.panes` | none | `job` | One row per pane, the fields of `roar panes --json` |
| `roar.unsubscribe` | `subscription` | `ok` | none |

`roar.result` takes `job` or `subscription`, `revision`, and `rows` or `error`. It answers `ok` at once and does no other work in its handler.

### Rules for every action

- **Reply fast, finish later.** A handler only checks and records. Rex hands a client one request at a time, holds at most 16 more, and starts each caller's `timeout_ms` only when its request reaches the front. So handler time multiplies across waiting callers. The target is 50 ms per handler, and roard logs every handler over 200 ms.
- **A `job` reply is not a receipt.** It means roard accepted the work. The stored record arrives later through `roar.result`.
- **Never call while handling.** No handler calls `client.perform`. A call in each direction, each waiting on the other's queue, would deadlock.
- **Results are hints; the store decides.** A sender that misses a `roar.result`, or reconnects, calls `roar.subscribe` again and reads the current records.
- **Deadlines include the queue and follow latency.** Rex starts `timeout_ms` only when a request reaches the front of the queue, so it covers the handler, not the wait. The sender's own deadline for each call covers both, and grows with the measured round trip ([Deadlines that follow latency](#deadlines-that-follow-latency)). A call that times out may still run later, so every action is idempotent.
- **Retries back off.** Every retry, reconnect and callback uses the backoff shape in [Configuration](#one-shape-for-every-backoff). Nothing retries in a tight loop, whatever the error.
- **Errors are Rex error codes.** A bad argument answers `bad_request`. A refusal answers `refused` with Roar's reason, such as `wrong_host`, `digest_mismatch` or `upload_conflict`. A failure inside roard answers `internal`.
- **The caller is checked.** roard refuses a caller whose principal kind is `anonymous`. It accepts a `peer` caller only with roard's own uid. It accepts a `tailscale` caller that Rex admitted: Rex lets such a caller run programs as this account already, so roard adds no trust that Rex did not give.
- **`from` is still a claim.** roard does not tie the envelope's `from` address to the caller's principal. That matches today's security model.

## Sending a message

1. The sender connects. It offers `roar.result` and resolves `roard` in one round trip, with both calls in flight. It needs no `roar.hello`: the part size follows from the welcome's message limit, because the sender and roard use the same Rex server.
2. It calls `roar.subscribe` with the message ID. This comes first, so no change can fall between enqueue and subscribe. A new ID answers with no record.
3. If the envelope and its files fit in one part, it calls `roar.enqueue`. Otherwise it uploads, as below, then calls `roar.upload.commit`.
4. A worker checks the digest, stores the message, and runs one delivery pass. roard publishes each committed change to the subscription, with a revision that only grows.
5. The sender prints the first settled result: `read`, `submitted`, `accepted`, `unknown`, `refused`, `expired`, or `queued` with a hold reason once the first delivery pass has finished. `accepted` is printed as `accepted`, never as `read`.
6. If nothing settles within `sender.print_deadline` (15 seconds by default), it prints the latest record it has. That is not a failure, and the sender never sends the message again because of it.

The receiver dedupes by message ID. A second enqueue or commit of a stored ID returns the stored record. It also makes sure the watcher covers that message, which `roar enqueue` does not do today.

### Uploads

- **One encoding layer.** The envelope travels as JSON. Each file travels separately as raw bytes, base64 once inside a part. A 12 MiB file is about 16 MiB on the wire.
- **Part size.** At most three quarters of the welcome's `max_message_bytes`: 768 KiB of base64 when the server allows 1 MiB messages, which leaves room for the JSON around it and for Rex's forwarded event. So a 12 MiB file takes 22 parts. `roar.hello` reports the same value as `max_part`.
- **One part in flight per upload.** The sender sends the next part after the last one's reply. Other senders' small requests stay fast, because each part's handler only appends and records.
- **Resumable.** `roar.upload.begin` is keyed by message ID, so calling it again returns the same upload and its committed offsets. A repeated part whose bytes match returns the committed offset. A part that conflicts is refused with `upload_conflict`. After a lost reply the sender calls `roar.upload.begin` again and continues from the committed offsets.
- **Durable.** roard keeps each upload under `uploads/<upload>/` in the store, mode 0700 with 0600 files, with its metadata. An upload survives a roard restart.
- **Bounded.** At most 8 uploads per host, at most 64 MiB reserved per host, and each upload at most the envelope's file limit. Bytes beyond a file's declared size are refused.
- **Expiry.** An upload expires 10 minutes after its last part, or 1 hour after it began. roard keeps one timer for the earliest expiry, re-arms it on progress, and rebuilds it from the stored metadata at startup. It also checks expiry when a part arrives.

### What the sender prints on failure

| Failure | Outcome |
| --- | --- |
| Rex resolves no client labelled `roard` on that host | `<id> refused roard_not_running`. Nothing was stored, so the outcome is known. |
| Rex resolves several | The sender asks `client.list` and uses the one that is not `lost`. Rex keeps a dropped client listed as `lost` for up to 60 s, so a restarted roard would otherwise share its label with its own old client. With several connected, `<id> refused roard_ambiguous`. roard sends a WebSocket close frame when it stops, so a clean restart leaves no `lost` client at all. |
| The connection drops, or a call fails with `client_unavailable` or `timeout`, before any `roar.enqueue` or `roar.upload.commit` was sent | The sender reconnects and resumes. If it cannot within the deadline, it prints `refused` with the reason. |
| The same, after an enqueue or commit was sent | The sender reconnects and calls `roar.subscribe`. If the record exists, it prints it. If roard cannot be reached within the deadline, it prints `<id> unknown remote_outcome_unknown`. A later error never turns that unknown into a refusal. |

The deadline is `sender.transfer.base` (2 minutes), plus the time the upload needs at `sender.transfer.min_rate` (256 KiB per second). Each acknowledged part extends it, so a slow but moving upload never times out. Between tries the sender backs off with `sender.connect.*`. If the connection stays open but carries nothing for `sender.stall_timeout` while a call waits, the sender treats it as dropped.

## The watcher inside roard

The watcher keeps its rules from SPEC.md: the same proof, the same states, and the same `watch.lock`. It changes from sweeping to reacting.

### What wakes it

| Event | Source | What roard does |
| --- | --- | --- |
| A record is created or changes | A filesystem event in the store's `notify/` directory. Every `Put` and every state change leaves an empty `notify/<id>` file after the change is durable. | Takes every mark, removing each before it reads the record, so a change made in between leaves a new mark. Publishes the record to subscribers. If it needs the watcher, roard adds its pane to the pending set and schedules an attempt. |
| An agent's status changes | Rex `block_event`: `program_status_changed`, `program_status_removed` | Schedules an attempt for that pane if it has pending work. An event that comes during an attempt runs one more attempt after it, because that attempt may have read the agent too early. |
| A pane's process changes or ends | Rex `block_event`: `process_changed`, `child_exited` | Schedules an attempt; a dead pane settles its messages |
| A pane or session closes | Rex `block_closed`, `session_destroyed` | Settles that pane's pending messages at once |
| roard starts or reconnects, or the filesystem watcher reports an overflow or error | roard itself | Reconciles: reads every active record and every pending pane |

- **Subscribe before snapshot.** roard sends `session.attach_all` and starts its filesystem watch before it reads the store, so a change made while it reads still produces an event. A change seen twice is applied once, by record revision.
- **Coalesce.** Several events for one pane within 50 ms schedule one attempt.
- **One watch.** roard watches only `notify/`, not each message's directory. On macOS kqueue opens every file of a watched directory, and a launchd agent gets 256 descriptors by default. A mark left while no roard runs waits until roard starts, and pruning a message removes its mark.
- **Event shape.** A session's events, `block_event` and `block_closed` among them, arrive inside a `session_event` with the session ID. Server events such as `client_left` arrive bare. roard unwraps the first kind.
- **Event scope.** `event_scope` `all` is required, because `block_event` carries agent status. roard filters by pane, so events for panes without pending work cost one map lookup. Rex offers no narrower scope.

### What still needs a timer

Two gaps have no event, so while work in one of them is pending, one timer covers it.

- **A lock owner dies.** When a native adapter or an old watcher holding a pane or `watch.lock` dies, the lock is released without any filesystem event.
- **Composer readiness after the agent rests.** Rex reports program status, not the composer's contents. A pane can rest with a draft that clears later without a status change.
- **A writer from an older Roar.** A Claude adapter started before this change keeps running the old binary, which leaves no notify mark. While a subscribed message is not final, the same timer reads it.

While any pending message is in one of these gaps, roard re-checks those messages every 30 seconds. With no such message, no timer runs.

### Migrating from the old watcher

An old watcher block may still hold `watch.lock`. roard retries the lock every 5 seconds until it gets it, and stops retrying once it has it. This retry exists only while an old watcher runs.

## Timers

Every timer that remains, and the ones this design removes. Inherited timers in other parts of Roar are listed for completeness. Every timer that remains has a key in [Configuration](#configuration).

| Timer | Decision | Key | Reason |
| --- | --- | --- | --- |
| Sender polls `roar.status` every 500 ms | Removed | | `roar.subscribe` and `roar.result` |
| Watcher sweeps every 30 s | Only while pending work is in a gap | `watcher.gap_recheck` | Filesystem and Rex events cover everything else |
| None | Every 60 s while anything is queued | `watcher.sweep` | Backstop for a wake-up that never came. It reads the store, not the pending flags, and stops when nothing is queued |
| Daemon scans the store every 30 s | Removed | | Filesystem events and reconciliation |
| Binary check every 30 s | Removed | | Install restarts the service; a manual build is seen through a directory watch |
| `roar-relay` scan every 10 min | Removed | | Startup inventory and session and block events |
| Upload expiry, 10 min idle and 1 h total | Kept, one timer | `roard.upload.idle_expiry`, `roard.upload.max_age` | Time passing has no event |
| roard's reconnect to its own Rex server | Kept, backoff | `roard.rex.reconnect.*` | No event arrives over a dead connection |
| Sender's connect and reconnect to another host | Kept, backoff | `sender.connect.*` | The same |
| roard's call back to a sender | Kept, backoff | `roard.callback.*` | The sender may be reconnecting |
| Per-call deadlines | Kept, scaled by measured latency | `sender.call.*`, `roard.callback.timeout`, `roard.callback.rtt_factor` | A fixed deadline fails on a slow link |
| Stalled connection | Kept | `sender.stall_timeout` | Detects a link that stays open but carries nothing |
| `RestartSec=2` under systemd | Kept | `roard.service.restart_delay` | Crash throttling by the service manager |
| Sender's 15 s print deadline | Kept | `sender.print_deadline` | How long the command waits before it prints the latest state |
| `sender.result_buffer` | 256 | 256 | S | `roar.result` calls held for one send. Beyond this a call is dropped, and the sender's resubscribe reads the store |
| `sender.settle` | 3s | 3s | S | How long a local send waits for a native adapter's outcome |
| `sender.settle_poll` | 100ms | 100ms | S | How often a local send reads that outcome |
| `target.elsewhere_timeout` | 5s | 20s | S | After a target misses here, how long to look for it on other hosts |
| `target.dns_timeout` | 2s | 10s | S | Lookup of an unstored host by its tailnet name |
| `panes.parallel` | 8 | 8 | S | Panes `roar panes` probes at once; each probe is about six Rex calls. roard reads it at start: needs a restart. |
| `store.retention` | 168h | 168h | S, D | How long a finished message stays, so a late reply finds its original. roard reads it at start: needs a restart. |
| `store.unread_per_recipient` | 2MiB | 2MiB | S, D | Unread body bytes one pane may hold. roard reads it at start: needs a restart. |
| `store.unread_per_host` | 16MiB | 16MiB | S, D | Unread body bytes the host may hold. roard reads it at start: needs a restart. |
| `store.files_per_host` | 256MiB | 256MiB | S, D | Unread attachment bytes the host may hold. roard reads it at start: needs a restart. |
| `store.unread_messages` | 1000 | 1000 | S, D | Unread messages the host may hold. roard reads it at start: needs a restart. |
| Sender's 2 min transfer deadline, extended by progress | Kept | `sender.transfer.*` | No event proves that a lost peer will answer |
| Rex ping and pong, 120 s and 60 s | Kept, Rex's own | none; Rex sets them | Detects a broken transport |
| `watch.lock` retry every 5 s | Only while an old watcher holds it | `watcher.legacy_lock_retry` | flock release has no event |
| `CatchUp` 2 s and reconnect sleeps of 1 s (`internal/fallback/watch.go`) | Removed | | One connection with subscribe before snapshot |
| Remote block polling every 100 ms, 20 s ready and 2 min completion timeouts (`internal/remote/run.go`) | Bootstrap install only | `install.*` | Normal sends no longer use blocks |
| Paste checks every 100 ms, `PasteWait` 2 s and `SubmitWait` 5 s (`internal/fallback/submit.go`) | Inherited, unchanged | `delivery.*` | They are proof deadlines. Program status cannot show the composer or a mid-turn submit. |
| Claude adapter 250 ms loop, 2 s grace, 30 s request gap (`internal/claude/mcp.go`) | Inherited, unchanged; separate follow-up | `claude.*` | Store and transcript events could replace the loop. That changes the adapter, not roard. |
| Codex `DeliverTimeout` 15 s and one stale-turn retry (`internal/codex/deliver.go`) | Inherited, unchanged | `codex.deliver_timeout` | A bounded call deadline. Daemon notifications are not yet proven usable. |

## Configuration

> **Every timer, limit and backoff is a setting, and changing one never needs a build.** A slow link, such as on a plane or a train, breaks loops that expect fast answers. The operator switches to the `slow` profile, or tunes one key, and the next command uses it. roard applies most changes without a restart.

### Where settings come from

| Source | Example | Notes |
| --- | --- | --- |
| Built-in defaults | the Default column below | Used when nothing else sets a key |
| A profile | `profile = "slow"` in the file, or `ROAR_PROFILE=slow` | Roar ships `default` and `slow`. The file can define more under `[profiles.<name>]`. |
| The config file | `$XDG_CONFIG_HOME/roar/config.toml`, default `~/.config/roar/config.toml`. `ROAR_CONFIG` names another path. | TOML. A missing file means defaults. |
| A host section | `[hosts.Baroni]` with `profile` and any `sender.*` key | Applies when the command talks to that host, named by its Rex host label |
| `ROAR_SET` | `ROAR_SET="sender.call.timeout=30s,sender.upload.part_size=256KiB"` | For one command or one shell. Wins over everything else. |

Later sources win: defaults, then the profile (`ROAR_PROFILE` before the file's `profile`), then the file's keys, then the host section's profile and keys, then `ROAR_SET`.

- Durations use Go's form, such as `750ms`, `30s` or `2m`. Sizes use `KiB` and `MiB`. Factors and fractions are plain numbers.
- An unknown key is a warning, never an error, so an older Roar can read a newer file.
- A value of the wrong type or out of range is an error. A command prints it and stops. roard logs it and keeps its last good settings.
- roard watches the config file's directory, so a saved change takes effect at once. The few keys that need a restart say so in the log: `roard.workers` and `roard.service.*`.

### The roar config command

| Command | Does |
| --- | --- |
| `roar config` | Prints every key with its value and where the value came from |
| `roar config get <key> [--host <label>]` | Prints one effective value |
| `roar config set <key> <value> [--host <label>]` | Writes the key to the file, keeping its other lines and comments |
| `roar config unset <key> [--host <label>]` | Removes the key from the file |
| `roar config profile <name> [--host <label>]` | Sets `profile` |
| `roar config check` | Reads the file and reports every warning and error, without running anything |

### One shape for every backoff

Each retry loop has the same five keys under its prefix.

| Key | Meaning |
| --- | --- |
| `initial` | The first delay |
| `factor` | Each delay is the last one times this |
| `max` | No delay is longer than this |
| `jitter` | A fraction from 0 to 1. Each delay is picked at random between `delay × (1 − jitter)` and `delay`, so many clients that fail together do not retry together. |
| `reset_after` | After the connection or call has worked for this long, the next failure starts again from `initial` |

A loop never retries faster than its backoff, whatever the error. A loop also stops at its own deadline, so backoff cannot run forever.

### Deadlines that follow latency

A call's deadline is the larger of a fixed floor and a multiple of the measured round trip:

`deadline = min(cap, max(timeout, rtt_factor × srtt))`

- `srtt` is the smoothed round trip. The sender measures it on `roar.hello`, each part's reply, and each `roar.result`, as `srtt = (1 − rtt_alpha) × srtt + rtt_alpha × sample`. It starts at the `roar.hello` sample.
- `cap` is what is left of the whole send's budget (`sender.transfer.*`). Rex's 30-second limit does not apply here: it bounds `timeout_ms`, which counts only from when the request reaches roard, on roard's own host. The sender passes `sender.call.remote_timeout` there, 10 seconds by default.
- So on a 2 s train link, a call waits 16 s with the default factor of 8, not 10 s. A spike to 20 s gives 160 s. A fast link keeps the 10 s floor.

The sender also passes its own needs to roard. `roar.subscribe` takes `hints`: `callback_timeout` and `callback_give_up`. roard uses them for its calls back to that sender, within `roard.callback.max_timeout` and `roard.callback.max_give_up`. So a sender on a slow link gets patient callbacks without any change on the receiving host.

### Keys

Who reads each key: **S** is the sender, **D** is roard, **C** is any command.

| Key | Default | `slow` | Read by | What it controls |
| --- | --- | --- | --- | --- |
| `sender.connect.timeout` | 10s | 45s | S | Floor of the deadline for opening the WebSocket and getting the welcome, two round trips. The deadline follows the last known round trip, and each failed try multiplies the floor by `sender.connect.factor`. |
| `sender.connect.initial` | 500ms | 2s | S | Reconnect backoff |
| `sender.connect.factor` | 2 | 2 | S | Reconnect backoff |
| `sender.connect.max` | 30s | 2m | S | Reconnect backoff |
| `sender.connect.jitter` | 0.5 | 0.5 | S | Reconnect backoff |
| `sender.connect.reset_after` | 60s | 5m | S | Reconnect backoff |
| `sender.call.timeout` | 10s | 30s | S | Floor of each call's deadline |
| `sender.call.rtt_factor` | 8 | 12 | S | Deadline as a multiple of `srtt` |
| `sender.call.rtt_alpha` | 0.125 | 0.125 | S | How fast `srtt` follows new samples |
| `sender.call.remote_timeout` | 10s | 10s | S | `timeout_ms` passed to Rex: roard's own time to answer once the call reaches it. At most 30s. |
| `sender.stall_timeout` | 45s | 3m | S | No message at all on the connection for this long, while a call waits, means reconnect |
| `sender.print_deadline` | 15s | 60s | S | Print the latest state after this |
| `sender.transfer.base` | 2m | 10m | S | The whole send's deadline before upload time |
| `sender.transfer.min_rate` | 256KiB/s | 16KiB/s | S | Upload time added to the deadline. Each acknowledged part extends it. |
| `sender.upload.part_size` | 768KiB | 128KiB | S | Base64 bytes per part, capped at three quarters of the server's message limit. Smaller parts lose less on a dropped link. |
| `sender.upload.in_flight` | 1 | 1 | S | Parts outstanding at once, in order on one connection. Raise it for a clean slow link; keep 1 for one that drops: see Evidence. |
| `roard.rex.reconnect.initial` | 1s | 1s | D | Backoff to the local Rex server |
| `roard.rex.reconnect.factor` | 2 | 2 | D | |
| `roard.rex.reconnect.max` | 30s | 30s | D | |
| `roard.rex.reconnect.jitter` | 0.2 | 0.2 | D | |
| `roard.rex.reconnect.reset_after` | 60s | 60s | D | |
| `roard.rex.connect_timeout` | 10s | 30s | D | One connection attempt to the local Rex server. roard reads it at start: needs a restart. |
| `roard.callback.timeout` | 10s | 30s | D | Floor of each `roar.result` call's deadline |
| `roard.callback.rtt_factor` | 8 | 12 | D | The same rule, with roard's own `srtt` per sender |
| `roard.callback.initial` | 250ms | 1s | D | Backoff when a callback fails, such as `client_unavailable` |
| `roard.callback.factor` | 2 | 2 | D | |
| `roard.callback.max` | 10s | 1m | D | |
| `roard.callback.jitter` | 0.5 | 0.5 | D | |
| `roard.callback.give_up` | 2m | 10m | D | Stop calling back. The sender's resubscribe still finds the record. |
| `roard.callback.max_timeout` | 30s | 30s | D | Cap on a sender's `callback_timeout` hint |
| `roard.callback.max_give_up` | 15m | 30m | D | Cap on a sender's `callback_give_up` hint |
| `roard.callback.idle` | 5m | 5m | D | A sender's callback queue ends after it has been empty this long; `client_left` ends it at once |
| `roard.callback.max_queued` | 1024 | 1024 | D | Results queued per sender. Updates for one message merge; beyond this the oldest is dropped and logged |
| `roard.workers` | 4 | 4 | D | Worker count. Needs a restart. |
| `roard.queue` | 1024 | 1024 | D | Jobs that may wait for a worker; more are refused as `roard_busy`. Needs a restart. |
| `roard.upload.max_count` | 8 | 8 | D | Open uploads per host |
| `roard.upload.max_bytes` | 64MiB | 64MiB | D | Bytes reserved by open uploads per host |
| `roard.upload.idle_expiry` | 10m | 1h | D | Delete an upload with no part for this long |
| `roard.upload.max_age` | 1h | 6h | D | Delete an upload this old |
| `roard.handler.warn` | 200ms | 200ms | D | Log a handler slower than this |
| `roard.log.max_size` | 1MiB | 1MiB | D | Log rotation |
| `roard.relay.retire` | true | true | D | Close and destroy `roar-relay` |
| `roard.service.restart_delay` | 2s | 2s | D | `RestartSec` and launchd's throttle. Applied by `roar setup`. |
| `watcher.coalesce` | 50ms | 50ms | D | Events for one pane within this window cause one attempt |
| `watcher.gap_recheck` | 30s | 30s | D | Re-check only for pending work that no event covers |
| `watcher.sweep` | 60s | 60s | D | Backstop while anything is queued, for a wake-up that never came |
| `watcher.legacy_lock_retry` | 5s | 5s | D | Only while an old watcher holds `watch.lock` |
| `install.deadline` | 2m | 10m | C | A whole `roar install` |
| `install.ready_check` | 100ms | 500ms | C | Bootstrap only: how often install reads the screen for `ROAR-READY` |
| `delivery.paste_check` | 100ms | 100ms | C, D | Inherited: paste proof checks |
| `delivery.paste_wait` | 2s | 2s | C, D | Inherited |
| `delivery.submit_wait` | 5s | 5s | C, D | Inherited |
| `claude.poll` | 250ms | 250ms | C | Inherited: the Claude adapter's loop |
| `claude.grace` | 2s | 2s | C | Inherited |
| `claude.request_gap` | 30s | 30s | C | Inherited |
| `codex.deliver_timeout` | 15s | 15s | C, D | Inherited |
| `agy.deliver_timeout` | 15s | 15s | C, D | Inherited: one `agentapi send-message` call |

`delivery.*`, `claude.*`, `codex.*` and `agy.*` act on this host's own panes, so `slow` leaves them alone. A slow link between hosts does not make a local paste slower. Each command reads them when it starts; roard reads them only when it starts, because changing them under running deliveries would race, and it logs that a change needs a restart.

### Latency between commands

Each command's sender measures the smoothed round trip to the host it talks to and keeps it in `<store>/rtt/<hash of the endpoint>`. The next command to that host starts from it, so its first deadlines, including the connect deadline, already fit the link.

### Example

```toml
# ~/.config/roar/config.toml
profile = "default"

[hosts.colonelk]
profile = "slow"            # Colonelk is reached over a weak link

[hosts.Baroni]
"sender.upload.part_size" = "256KiB"

[profiles.train]
"sender.call.rtt_factor" = 16
"sender.stall_timeout" = "5m"
```

On the train: `ROAR_PROFILE=train roar send Baroni:claude@stance "status?"`, or `roar config profile train` until the trip ends.

## Install and setup on another host

`roar install <host>` must work before roard exists on that host, so it still uses a detached block.

1. It subscribes to that host's events with `event_scope` `session`, then picks the first session the host's `session.list` returns.
2. If the host has no session, it creates one with `session.create`, label `roar-install`, and no windows. It destroys that session when it is done.
3. It copies the binary as today, then runs `roar setup` there, which installs the service and restarts roard.
4. It learns that each step's block finished from `child_exited`, then reads its screen once. Only the wait for `ROAR-READY` still polls the screen, because Rex has no event for new terminal output. The whole install has a 2-minute deadline.

Setup is not a roard action, because it restarts roard.

## Retiring roar-relay

1. Install the new Roar on Stiletto, Baroni and Colonelk with `roar install`.
2. When roard starts, it lists the blocks of the session labelled `roar-relay`. After that it follows `block_created`, `block_closed` and `session_destroyed` for that session.
3. roard closes the relay's idle shell: a placed shell block whose foreground process is the shell itself. Every relay has one, so the session never empties without this step.
4. When the relay holds no detached block, roard destroys it. A detached block that remains, such as an old watcher with a long `wrong_program` hold, keeps it until that block closes.
5. Until all three hosts run roard, the `roar enqueue` command stays, so an old sender can still reach a new host. It goes in the next release after that.

## Worked example

**Stiletto sends a 12 MiB file to an idle Claude on Baroni.**
`roar send --file big.bin claude@stance "see attached"` resolves the pane through Baroni's Rex server, as today.
It opens one control connection to Baroni's server, offers `roar.result`, finds the client `roard`, and calls `roar.subscribe`.
It calls `roar.upload.begin`, then 22 `roar.upload.part` calls of 768 KiB, then `roar.upload.commit`.
A worker on Baroni checks the digest and stores the message. roard calls `roar.result` with the record, `queued`.
The worker's delivery pass finds Claude's channel connected, so the record becomes `accepted`, then `read`. roard calls `roar.result` for each change.
Stiletto prints `<id> read` and closes its connection. No session or block was created on either host.

## Acceptance criteria

These are the tests to write first. They extend the numbered list in SPEC.md.

42. `roar daemon` connects to the local Rex server, and `target.resolve` finds one client labelled `roard` that lists every action in the table.
43. A second `roar daemon` on the same host exits with status 0 and leaves one `roard` client.
44. After the Rex server restarts, roard reconnects, offers its actions again, and reconciles without delivering any message twice.
45. A caller with an `anonymous` principal, or a `peer` with another uid, gets `refused`.
46. A send to another host creates no Rex session or block on either host, and makes no `roar.status` call.
47. A 12 MiB attachment arrives intact in at most 25 part calls. A conflicting part, or a final digest that does not match, is refused and stores nothing.
48. After a part's reply is lost, the sender resumes from the committed offset and sends no completed part again.
49. A repeated enqueue or commit of a stored ID returns the stored record, and the watcher covers that message afterwards.
50. A record committed between `roar.subscribe` and its reply reaches the sender exactly once, even when the change is published twice.
51. A sender that loses its connection after commit reconnects and prints the current record, without a second delivery.
52. Every handler answers within 50 ms while an agent is busy, while a 12 MiB upload runs, and while a worker is stalled on disk.
53. A held message is delivered when the pane comes to rest, with no other command run and no sweep timer firing.
54. With no pending work, roard fires no timer: no store, binary or status checks.
55. With no roard on the target host, `roar send` prints `refused roard_not_running` and stores nothing there.
56. `roar status` for a message on another host works after the recipient's session is closed.
57. `roar setup` writes the launchd or systemd service, starts roard, and prints `ok` on a second run.
58. roard closes the relay's idle shell, destroys an empty `roar-relay`, and keeps one that still holds a detached block until that block closes.
59. After `roar install` replaces the binary, the restarted roard runs the new version.
60. With no config file, every key has its default, and `roar config` names `default` as each value's source.
61. Precedence holds: a profile overrides defaults, a file key overrides the profile, a host section overrides the file for that host only, and `ROAR_SET` overrides all. `roar config` names the winning source.
62. An unknown key is a warning. A bad value stops a command with a message naming the key. roard logs it and keeps its last good settings.
63. roard applies a saved change to a live key, such as `watcher.coalesce`, without a restart. For `roard.workers` it logs that a restart is needed.
64. Every backoff follows its keys: with jitter 0, the delays are exactly `initial × factor^n` capped at `max`, and `reset_after` restarts the sequence. With jitter, each delay stays within its range.
65. A call's deadline is `max(timeout, rtt_factor × srtt)`, capped by what is left of the send's budget, never by Rex's 30 seconds. Over a link with 2 seconds of added delay, a send with the `slow` profile completes, and with `default` it still completes, by backing off rather than flooding.
66. A sender's `hints` set roard's callback deadline and give-up time for that sender, within the caps.
67. `roar config set` and `unset` keep the file's other lines and comments.

| Criteria | Tests |
| --- | --- |
| 42, 43 | `TestOneRoardPerStore` |
| 44 | `TestSessionReconnectsAndRunsSetup`, `TestSessionBacksOffOnDialFailure` (`rexctl` in github.com/vizkid/rexkit); live run |
| 45 | `TestCallerCheck` |
| 46, 50 | `TestEnqueueCallsBackEachChange`, `TestPeerSendSettlesByCallback` |
| 47 | `TestUploadRoundTrip`, `TestUploadRefusesConflictsAndGaps`, `TestUploadCommitChecksDigest`, `TestPeerSendsLargeAttachmentInFewParts` |
| 48 | `TestUploadResumes`, `TestPeerResumesAfterDrop`, `TestPeerSurvivesSlowLossyLink` |
| 49 | `TestDuplicateEnqueueIsCovered` |
| 51 | `TestPeerResumesAfterDrop`; live run |
| 52 | `TestHeldSendWarnsWithoutRoard`, `TestStoredMessageWakesWatcher` |
| 53 | `TestRoardDeliversHeldSend`, `TestRexEventRouting` |
| 54 | `TestGapTimerOnlyWhenNeeded` |
| 55 | `TestPeerWithoutRoard` |
| 56 | `TestStatusJob`, `TestPeerStatus` |
| 57 | `TestServiceDarwin`, `TestServiceLinux` |
| 58 | `TestRetiresRelay` |
| 59 | live run |
| 60–64 | `internal/config`: `TestDefaultsWithoutFile`, `TestPrecedence`, `TestUnknownKeyWarnsBadValueFails`, `TestBackoffSequence`, `TestBackoffJitterRange`; 63 in the live run |
| 65 | `TestDeadlineFollowsLatency`, `TestPeerSmallSendOverSlowLink`, `TestPeerFirstSendOverVerySlowLink` |
| 66 | `TestCallbackHintsSetGiveUp` |
| 67 | `TestSetKeepsComments`, `TestConfigCommand` |

The live run on Stiletto, Baroni and Colonelk reports, for a small send and for a 12 MiB send: process spawns, control calls, encoded bytes, median and 95th-percentile latency, handler time, queue wait, and idle wakeups per hour.

## Evidence

| Claim | Evidence |
| --- | --- |
| A client can offer actions that other clients call | Live on Stiletto, 2026-11-01, API revision 7. A prototype connected, set label `roard`, and listed `roar.ping` in `client.keymap`. `client.perform` returned its result in 18 ms. An action it did not list failed with `not_supported` before reaching it. |
| Another host can call it, and Rex names the caller | The same prototype, called from Baroni with `rex -S https://stiletto-rex.tail4f2cce.ts.net api call client.perform`. The principal was kind `tailscale`, node `baroni`, with the Tailscale user ID. A local call showed kind `peer`, uid 501. |
| The `rex` CLI refuses large requests | `client.perform` with 512 KiB and 900 KiB arguments failed in the CLI: "encoded request is 524459 bytes, limit is 65536". The server's welcome reports `max_message_bytes` 1048576. |
| Roar's own client reaches another host over Tailscale and sends large messages | `TestLiveRemoteControl` (`rexctl` in github.com/vizkid/rexkit, run with `REXKIT_LIVE=1`), 2026-11-01, from Stiletto. Against Baroni: a client offered an action on Baroni's server, another called it; small call 16 ms, 700 KiB call 840 ms, principal kind `tailscale`. Against Colonelk: 13 ms and 568 ms. |
| Baroni and Colonelk support it | Both servers list `client.keymap`, `client.perform`, `client.reply` and `client.set_label`. roard itself has not run on Linux yet. |
| Terminal events | The local server's terminal block type lists `program_status_changed`, `program_status_removed`, `process_changed` and `child_exited`. `block_event` arrives only with `event_scope` `all`. |
| A block data connection cannot carry an upload | The reference's "Data Connections" section: bytes flow only from the server to the client. |
| Sends survive a slow, dropping link | `TestPeerSurvivesSlowLossyLink` (`ROAR_SLOW=1`), 2026-11-01: a proxy in front of the local Rex socket delays every chunk 1 s each way and cuts each connection after about 1 MiB. 4 MiB with `in_flight` 1: 2 min 13 s, 6 drops, 5.4 MB sent, file intact. With `in_flight` 4: 6 min 50 s, 17 drops, 13.2 MB sent: a deep window loses the whole batch at each drop. A small send over the 2 s round trip settles in 20 s with default settings. |
| A first send needs no learned round trip | `TestPeerFirstSendOverVerySlowLink` (`ROAR_SLOW=1`): over a 4 s round trip with a 3 s connect floor, tries of 3 s and 6 s fail and the 12 s one connects; the send settles in 38 s. |
| roard delivers a held send with no other command | `TestRoardDeliversHeldSend` (`internal/cli`): a test agent that reports `working` holds an inform as `busy`; when it reports `done`, roard's watcher submits it, woken by Rex's `program_status_changed` on its own connection. |
| 12 MiB in few calls | `TestPeerSendsLargeAttachmentInFewParts`: 22 parts and 28 calls in all, where the CLI path needed about 456 writes. |
| A windowless session still shows as a project | Viz checked the Rex sidebar on 2026-11-01. |
| roard runs as a service on all three hosts | Installed 2026-11-02. On each host roard closed the idle `roar-relay` shell and destroyed the session within a second of starting. `roar panes Baroni` from Stiletto answered through roard in 0.56 s. On Colonelk roard ran beside 59 local sends; every one reached `submitted`. |
| A message behind a held one answers at once | Live, 2026-11-02: a 3 MiB send from Baroni queued behind a small one held as `busy`. With no reason of its own, the sender waited its whole 15 s print deadline and printed `queued`. The delivery pass now holds every later message in that pane as `behind`, still in order (`TestBlockHoldsLaterMessagesBehind`). `behind` needs no gap re-check: the message ahead of it decides that. The attachment arrived intact, SHA-256 `4952b5fa…`. |
| A service needs no command deadline | The first install exposed it: every command ran under a 2-minute deadline, so launchd restarted roard every 120 s, and each restart left a `lost` client that made senders fail with `roard_ambiguous`. `TestServiceCommandsHaveNoDeadline`, `TestResolveSkipsLostClient` and `TestCloseLeavesAtOnce` pin the three fixes. |
| A host name need not be stored | Stored host lists differ per machine: Colonelk had none. An unstored `<Label>:` tries this host's own naming pattern from `server.status` (`colonelk-rex.<tailnet>` gives `baroni-rex.<tailnet>`) and checks DNS. If that fails, the sender gets `unknown_host` with the `rex hosts add` fix, not `no_such_target`. `TestTailnetCandidate`, `TestUnknownHostPrefixSaysSo`. |
| A miss names the host that has the pane | Live, 2026-11-03: from Stiletto, `comm@stance` gave `no_such_target`, because Stiletto has its own `stance` session and stance comm is on Baroni. The agent asked its user instead of trying Baroni. A target with no `<host>:` that misses here is now tried on every stored host, for at most 5 s, and the error names each match, such as `on another host: Baroni:comm@stance`, with the candidates when it is ambiguous there. Roar does not send there itself: one label on two hosts may be two agents. `TestMissNamesTheHostThatHasIt`, `TestMissDoesNotSendElsewhere`, `TestElsewhere`. |
| The API reference | Each Rex server serves it at `GET /llms.txt` (`curl --unix-socket <socket> http://localhost/llms.txt`). The public docs do not cover client actions. |

## Open questions

**Stranded pane: cause found, 2026-11-03.** On 2026-11-02 four messages stayed queued for a real Claude pane through its rests and its close, until a roard restart. There were two causes. First, roard ignored every Rex event: it matched `block_event` and `block_closed` at the top level, but the server wraps both in a `session_event`. The unit tests fed the bare shape, and the end-to-end tests ran roard with a temporary `HOME`, so it never reached Rex. No host had ever logged a `block_closed` line. Second, with events working, an event that came during an attempt was lost: the attempt it caused found the message lock held, and the running attempt finished with what it had read before the event. `TestRoardDeliversEachHeldSendInTurn` failed 2 times in 10 before the fixes and 9 in 25 with roard connected, and 0 in 40 after. `TestRexEventRouting`, `TestBlockClosedSchedulesAttempt` and `TestEventDuringAttemptRunsAgain` pin the fixes. `watcher.sweep` stays as the backstop.

**Code review, 2026-11-03.** A Codex peer (GPT-6-Astra, started with `smithers spawn`) reviewed 930a9ee and found 16 problems; all are fixed in 17ff1c4 through 99c2712, each with a test that failed first. The ones that change behaviour an operator can see: text after `--` is never parsed as flags; `roar config set` writes the parsed value and `config set`/`unset` find a key in any TOML spelling, while a key set twice in two spellings is refused at load; `roar install` honours `install.*` for the target host; a send's budget bounds every dial and call of a try; a sender's callback queue ends when the sender leaves or after `roard.callback.idle`. Every key in the table above is read by code, and `TestEveryKeyIsRead` keeps it so.

| Question | Recommendation |
| --- | --- |
| Rex closes a new connection whose hello arrives more than 5 s after it opened. The hello can leave only after the upgrade reply arrives, so it reaches the server one round trip later. No Rex client, the `rex` CLI included, can open a connection over a round trip of about 5 s or more. | A Rex change: a longer or configurable hello deadline. Roar cannot work around it without a persistent connection that outlives the spike. Connections already open survive slower links. Seen 2026-11-01: 4 s round trip opens in 8 s; 5 s and 12 s fail before the welcome. |
| What part size works best? | Start at 768 KiB, one part in flight. Measure small-send latency during a large upload before raising either. |
| Should a remote send fall back to a detached block when roard is down? | No. That brings `roar-relay` back and hides a broken service. Refuse with `roard_not_running`. |
| Can Codex daemon notifications replace the stale-turn recheck? | Unknown. Roar's Codex client ignores notifications today. Test them before relying on them. |
| Does the Rex app show a connected `roard` client anywhere? | Not seen in the prototype. Check during the live run. |
| Which TOML library? | Settled: `github.com/BurntSushi/toml` v1.6.0 reads the file; `roar config set` edits it line by line, so comments survive. |
| How to test slow links? | Settled: a proxy in front of the Rex socket (`internal/remote/lossy_test.go`) adds latency per chunk and cuts connections. Interface tools such as `dnctl` or `tc netem` need root. |
| Should Roar tune itself, for example switch to `slow` when `srtt` stays high? | Not in this pass. The latency rule already stretches deadlines. Measure first, then decide. |

## Related documents

- [SPEC.md](SPEC.md): the watcher, Across hosts, Setup, and Security model, which this replaces in part.
- Rex control API: `curl --unix-socket "$HOME/Library/Application Support/rex/server.sock" http://localhost/llms.txt`, sections "The Control Connection", "Data Connections", `session.attach_all`, `client.keymap`, `client.perform`, `client.reply`, and `client_perform_requested`.
- Peer reviews: roar messages `rm-01M6BMF801BK63D3XG3NZHH495` (the relay proposal) and `rm-01M6BPVVM6NJ85SMD3N6Y70C4A` (this draft, performance and events). Their findings shaped the install path, the status rule, the duplicate fix, the completion protocol, the uploads, and the timer list.
