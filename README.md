# roar

Message routing between [Rex](https://www.superlogical.com/rex/) sessions and panes.

Roar delivers through an agent's native channel when one is connected: Claude Code channels, or the Codex App Server daemon that every Codex TUI already uses.
Without one, it types through Rex, but only after proving that the pane holds an agent with an empty composer.
Otherwise it holds the message. There is no daemon: the commands share a directory of files.

The design and its evidence are in [docs/SPEC.md](docs/SPEC.md).

## Install

```sh
go build -o ~/.local/bin/roar ./cmd/roar
roar setup                         # add Roar to every Claude and Codex config root in $HOME
roar install <rex-host-label>      # copy it to another Rex host's ~/.local/bin and run setup there

# Another platform, for example a Linux host from a Mac:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o roar-linux-amd64 ./cmd/roar
roar install --binary roar-linux-amd64 <rex-host-label>
```

Roar never wraps an agent. `roar setup` puts it in each agent's default config, so a plain `claude` or `codex` gets it:

| Config root | Found as | What setup adds |
| --- | --- | --- |
| Claude | `~/.claude`, `~/.claude-*`, `$CLAUDE_CONFIG_DIR` | The `roar` skill, and a user-scope MCP server `roar` (`roar mcp`) |
| Codex | `~/.codex`, `~/.codex-*`, `$CODEX_HOME` | The `roar` skill, and a `SessionStart` hook (`roar hook codex`) that records which thread each pane shows, for a Codex that runs in its own pane process |

Run it again after you move the binary. It changes only what differs.
Two steps stay with you, because each one is a consent the agent asks for:

1. Start Claude with `--dangerously-load-development-channels server:roar`, for example in the shell function or alias that already starts `claude`. Claude loads channels only from that flag.
2. Run `/hooks` once in each Codex home and trust the Roar hook. Codex does not run a new hook until you do. A Codex on the shared daemon runs hooks in the daemon, so there Roar instead learns each thread's pane from the first Roar message the thread receives. Until then, `roar` run by that Codex refuses with `unbound_codex_thread` rather than send under the wrong pane.

Without them, messages still arrive by typing through Rex.

## Use

Send from any Rex pane:

```sh
roar send api-work "Schema change merged; rebase before you push."
roar send api-work/tests "Run the integration suite."   # a named pane
roar send Baroni:api-work "Same, on another Rex host."
roar send --request api-work "Can you review PR 12?"
roar send --file report.md api-work "Report attached."
echo "long body" | roar send api-work

roar status                      # your sent messages
roar inbox                       # messages for this pane
roar read <id> --mark
roar send --reply <id> "Done."
```

The `roar` skill teaches the agent to answer messages and send its own.
`roar send` prints `<id> <state> [reason]`, so neither you nor an agent needs a follow-up `roar status`.

A target is a Rex session, tab (window) or pane label, `tab@session` (or `session/tab`), or a block ID. Put `host:` in front for another Rex host, for example `baroni:claude@burns`, or the host's DNS name when Rex has no label for it. This machine's own name works too, so an address copied from another host's message can be sent back from here.
Where no label matches, a hint names the pane: the agent it runs (`codex@roar`, `claude@roar`, `devin@roar`, `agy@roar`) or where it sits in its tab (`left`, `right`, `top`, `bottom`, `top-left`, and so on). `peer` on its own names the other pane in your tab, so two agents side by side can write `roar send peer "..."`. A label always wins over a hint.

| The recipient is | Delivery |
| --- | --- |
| Claude started with the `server:roar` channel flag | Native, with provider receipt (`read`) |
| Codex in a home where the Roar hook is trusted | Native: a new turn when idle, a steer when busy (`read`) |
| Antigravity CLI (`agy`) after it ran `roar bind agy` (or any `roar` command) | Native through `agy agentapi send-message`: a new turn when idle, joins the turn when busy (`read`). The session token stays in a 0600 file on that host. |
| Any other Claude, or an unbound agy | Typed through Rex when the agent is at rest with an empty composer (`submitted`); otherwise held |
| Any other Codex, or Devin CLI | Typed through Rex when the composer is empty (`submitted`). Mid-turn, Enter adds the message to the running turn (proof `rex_steer`). |
| Anything else | Held; never typed into |

When Roar holds a typed message, it starts a watcher on that host if none runs. The watcher delivers the message when the pane comes to rest, then exits once nothing is held. It runs as `roar deliver --watch --until-idle` in a `roar-watch` block of the `roar-relay` session.
`roar ready <target>` reports whether a pane could take typed input now (exit 0 yes, 1 wait, 2 never).
A message whose delivery outcome is unknown is never retried automatically. Check the conversation, then run `roar resolve <id> --read` or `--requeue`.

## Checks

```sh
scripts/check.sh                 # format, vet, tests, 300-line file limit
ROAR_LIVE=1 ROAR_LIVE_DIR=<dir both agents trust> go test ./... -run Live   # Codex needs roar setup and a trusted hook
```

Most tests run real Rex panes. The live tests drive real Claude Code and Codex and spend a few short model turns.

## Licence

MIT. See [LICENSE](LICENSE).
