# deepcove

A web dashboard for running coding agent sessions in isolated sandboxes across your own machines.

deepcove runs each agent in a sandbox on one of your machines and puts its terminal in your browser.
The dashboard shows a live preview of every session, so you can see what each agent is doing without
opening it. Sessions keep running after you close the browser, and a session on another machine
behaves exactly like one on your laptop.

## Sandboxes

**Virtual machine** (Apple Virtualization on macOS, Cloud Hypervisor on Linux). Each session boots its
own Linux guest with its own kernel, so it is isolated from the host and from every other session.

**No sandbox**. Runs on the machine itself with your files, network and logins. Nothing is isolated.

## Your machines

One machine needs nothing but deepcove. To use more than one, sign each of them into your Tailscale
network and they find each other on their own: the dashboard lists every session on every machine, and
starting one somewhere else is the same click.

Nothing is published to the internet. The dashboard listens on 127.0.0.1, and over your tailnet only
your own Tailscale identity is let in.

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
make run    # build everything, then ./deepcove start
make test
make lint
```

## License

Apache 2.0. See [LICENSE](LICENSE).
