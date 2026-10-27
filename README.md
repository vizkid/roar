# roar

Message routing between [Rex](https://www.superlogical.com/rex/) sessions and panes.

Roar delivers through an agent's native channel when one is connected: Claude Code channels, or the Codex App Server.
Without one, it types through Rex, but only after proving that the pane holds an agent with an empty composer.
Otherwise it holds the message. There is no daemon: the commands share a directory of files.

The design and its evidence are in [docs/SPEC.md](docs/SPEC.md).

## Install

```sh
go build -o ~/.local/bin/roar ./cmd/roar
roar install <rex-host-label>      # copy it to another Rex host's ~/.local/bin
```


## Use

Start agents through Roar so they receive messages natively:

```sh
roar claude [claude args...]     # accept the development-channel prompt once
roar codex [codex args...]
```

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

`roar claude` and `roar codex` install a `roar` skill in the agent's config root (`~/.claude/skills/roar`, `~/.codex/skills/roar`, or under `CLAUDE_CONFIG_DIR` or `CODEX_HOME`).
The skill teaches the agent to answer messages and send its own.
`roar send` prints `<id> <state> [reason]`, so neither you nor an agent needs a follow-up `roar status`.

A target is a Rex session, tab (window) or pane label, `tab@session` (or `session/tab`), or a block ID. Put `host:` in front for another Rex host.

| The recipient was started with | Delivery |
| --- | --- |
| `roar claude` or `roar codex` | Native, with provider receipt (`read`) |
| Plain `claude` or `codex` | Typed through Rex when the composer is empty (`submitted`); otherwise held |
| Anything else | Held; never typed into |

Held messages go out on the next send to that pane, on `roar deliver`, or automatically while `roar deliver --watch` runs on that host.
`roar ready <target>` reports whether a pane could take typed input now (exit 0 yes, 1 wait, 2 never).
A message whose delivery outcome is unknown is never retried automatically. Check the conversation, then run `roar resolve <id> --read` or `--requeue`.

## Checks

```sh
scripts/check.sh                 # format, vet, tests, 300-line file limit
ROAR_LIVE=1 ROAR_LIVE_DIR=<dir both agents trust> go test ./... -run Live
```

Most tests run real Rex panes. The live tests drive real Claude Code and Codex and spend a few short model turns.

## Licence

MIT. See [LICENSE](LICENSE).
