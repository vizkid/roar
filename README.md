# ping

Message routing between [Rex](https://www.superlogical.com/rex/) sessions and panes.

Ping delivers through an agent's native channel (Claude Code channels, the Codex App Server) when one is connected.
Without one, it types through Rex, but only after proving the pane holds an agent with an empty composer.

Status: under construction. See [docs/SPEC.md](docs/SPEC.md).

## Checks

```sh
scripts/check.sh               # format, vet, tests, file-size limit
PING_LIVE=1 go test ./... -run Live   # live tests against real Rex panes and agents
```

## Licence

MIT. See [LICENSE](LICENSE).
