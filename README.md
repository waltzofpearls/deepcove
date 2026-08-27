# deepcove

A self-hosted studio for running coding agents in isolated sandboxes, on machines you own.

Every session gets its own sandbox, and the agent runs inside it with your code. You drive it from a
web terminal in the dashboard. Nothing runs the agent on your own machine, and nothing leaves your
machines. Add Tailscale and the same dashboard reaches every node you run it on, from anywhere,
without exposing anything to the internet.

## Status

Early, and moving fast. There are no releases yet, so building from source is the only way to run it.

## Sandboxes

**Virtual machine** (macOS, Apple Virtualization). Each session boots its own Linux guest with its own
kernel, so it is isolated from the host and from every other session.

**Docker**. Shares the host kernel, starts fastest, and keeps the agent's login between sessions.

## Build

You need Go and Node, at the versions pinned in `go.mod` and `ui/package.json`.

```sh
cd ui && npm install   # once, for the dashboard build
make build
```

On macOS the build signs the binary with no certificate, which is what lets it start virtual machines.

## Run

```sh
./deepcove start
```

Then open http://127.0.0.1:9000.

To keep it running in the background, starting again with your login session:

```sh
./deepcove service install
./deepcove service status
```

`deepcove service uninstall` removes it and leaves your data alone.

## Upgrade

```sh
./deepcove upgrade --check
./deepcove upgrade
```

It verifies the download against the release's checksums before replacing anything, and restarts the
service if you installed one. If Homebrew put the binary there, it tells you to use `brew upgrade`
instead of overwriting a file Homebrew tracks.

## Develop

```sh
make dev    # Go with live reload plus the Vite dev server, in docker compose
make test
make lint
```

`make dev` expects `make bootstrap` to have installed `ui/node_modules` in a Linux container first.

## License

Apache 2.0. See [LICENSE](LICENSE).
