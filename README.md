# ping

Message routing between [Rex](https://www.superlogical.com/rex/) sessions and panes.

Ping delivers through an agent's native channel when one is connected: Claude Code channels, or the Codex App Server.
Without one, it types through Rex, but only after proving that the pane holds an agent with an empty composer.
Otherwise it holds the message. There is no daemon: the commands share a directory of files.

The design and its evidence are in [docs/SPEC.md](docs/SPEC.md).

## Install

```sh
go build -o ~/.local/bin/ping ./cmd/ping
ping install <rex-host-label>      # copy it to another Rex host's ~/.local/bin
```

On macOS and Linux the system already has an ICMP `ping` in `/sbin`. Wherever `~/.local/bin` comes first in PATH, this `ping` shadows it.

## Use

Start agents through Ping so they receive messages natively:

```sh
ping claude [claude args...]     # accept the development-channel prompt once
ping codex [codex args...]
```

Send from any Rex pane:

```sh
ping send api-work "Schema change merged; rebase before you push."
ping send api-work/tests "Run the integration suite."   # a named pane
ping send Baroni:api-work "Same, on another Rex host."
ping send --request api-work "Can you review PR 12?"
ping send --file report.md api-work "Report attached."
echo "long body" | ping send api-work

ping status                      # your sent messages
ping inbox                       # messages for this pane
ping read <id> --mark
ping send --reply <id> "Done."
```

A target is a Rex session label, optionally followed by `/pane` and preceded by `host:`, or a block ID.

| The recipient was started with | Delivery |
| --- | --- |
| `ping claude` or `ping codex` | Native, with provider receipt (`read`) |
| Plain `claude` or `codex` | Typed through Rex when the composer is empty (`submitted`); otherwise held |
| Anything else | Held; never typed into |

Held messages go out on the next send to that pane, on `ping deliver`, or automatically while `ping deliver --watch` runs on that host.
`ping ready <target>` reports whether a pane could take typed input now (exit 0 yes, 1 wait, 2 never).
A message whose delivery outcome is unknown is never retried automatically. Check the conversation, then run `ping resolve <id> --read` or `--requeue`.

## Checks

```sh
scripts/check.sh                 # format, vet, tests, 300-line file limit
PING_LIVE=1 PING_LIVE_DIR=<dir both agents trust> go test ./... -run Live
```

Most tests run real Rex panes. The live tests drive real Claude Code and Codex and spend a few short model turns.

## Licence

MIT. See [LICENSE](LICENSE).
