# Spike S4: gvisor-tap-vsock host<->guest networking (macOS)

## Question

Does gvisor-tap-vsock, embedded as a Go library in the host process (no
external `gvproxy`/`vfkit` binaries), provide the guest's network for a vz
microVM and let a host TCP port reach a guest port — the pattern rask needs
to expose the real kube-apiserver (guest:6443) on a host ephemeral port?

## Result

**Yes, on the first attempt and every attempt.** Across 10 runs, `vm.Start()`
-> first successful HTTP response through the host->guest forward: p50 =
140.3ms, p95 = 142.9ms, min = 126.8ms, max = 142.9ms. This is only ~15ms
over S2's pure-boot p50 (125ms) — bringing up eth0 and answering through the
gvisor-tap-vsock forward costs almost nothing on top of boot itself.
Outbound NAT and DNS through the same stack also worked, first attempt,
both times it was checked (status 200 from `http://example.com`).

## Environment

- Host: Apple Silicon (arm64), macOS 26.5.2 (build 25F84), Darwin 25.5.0
- Go: `go1.27-devel`, `darwin/arm64`
- `github.com/Code-Hex/vz/v3` v3.7.1 (same pin as S2)
- `github.com/containers/gvisor-tap-vsock` v0.8.9
- `github.com/vishvananda/netlink` v1.3.1 (guest-side interface/route config)
- Spike module: `spikes/s4/go.mod` (`module rask-spike-s4`), independent of
  the repo root and of spikes/s1, s2. Kernel reused read-only from S2
  (`spikes/s2/work/Image`, puipui-linux v1.0.3 — see S2's `fetch.sh` for the
  selection rationale, unchanged here).

## The wiring (rask's production recipe)

```
                 host process
  ┌─────────────────────────────────────────────────────┐
  │  gvisor-tap-vsock virtualnetwork.New(cfg)            │
  │    - userspace gVisor tcpip.Stack, NIC "1"           │
  │    - DNS :53, DHCP, TCP/UDP forwarder all on NIC 1   │
  │    - cfg.Forwards["127.0.0.1:<port>"]="192.168.127.2:6443"
  │      -> real host net.Listen("tcp", ...) bound        │
  │         synchronously inside New(), proxies into      │
  │         the gvisor stack via gonet.DialContextTCP     │
  │                                                        │
  │  vn.AcceptVfkit(ctx, hostConn)  <-- goroutine          │
  │           ▲                                            │
  │           │ hostConn = net.FileConn(hostFile)          │
  │  syscall.Socketpair(AF_UNIX, SOCK_DGRAM) -> fds[0],[1] │
  │           │ guestFile = os.NewFile(fds[0])             │
  │           ▼                                            │
  │  vz.NewFileHandleNetworkDeviceAttachment(guestFile)    │
  │  vz.NewVirtioNetworkDeviceConfiguration(attachment)    │
  │  config.SetNetworkDevicesVirtualMachineConfiguration   │
  │  vm.Start()                                            │
  └─────────────────────────────────────────────────────┘
                        │ virtio-net (vz)
                        ▼
                 guest (rask-init, PID 1)
     netlink: eth0 up, 192.168.127.2/24, default route via
     192.168.127.1, /etc/resolv.conf -> nameserver 192.168.127.1
     http.Server on :6443 (apiserver stand-in)
```

Key pieces, exact API calls:

**Host <-> guest transport — connected unixgram socketpair, not a
filesystem socket.** `syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM,
0)` gives two already-connected fds. `fds[0]` becomes an `*os.File` handed
straight to `vz.NewFileHandleNetworkDeviceAttachment(file)`, which asserts
(via `getsockopt(SOL_SOCKET, SO_TYPE)`) that the fd is `SOCK_DGRAM` and
already connected — a socketpair fd satisfies this natively, no
`connect()`/bind step needed. `fds[1]` becomes an `*os.File` too, but is
converted to a `net.Conn` with `net.FileConn(hostFile)` (which dups the fd
internally — the original `*os.File` can be closed right after) and that
`net.Conn` is passed to `vn.AcceptVfkit(ctx, conn)`.

**Why this framing matches with zero adapter code.** Reading
`pkg/tap/switch.go` in gvisor-tap-vsock: `types.VfkitProtocol` maps to
`vfkitProtocol{}`, whose `Stream()` returns `false` — meaning
`Switch.rxNonStream` just does raw `conn.Read(buf)` / `conn.Write(buf)` per
Ethernet frame, no length-prefix framing (unlike `QemuProtocol`, which is
stream-oriented with a 4-byte big-endian length header). This is exactly
what `VZFileHandleNetworkDeviceAttachment` expects/produces on its own
datagram socket (per Apple's docs: "raw packets... at the level of the data
link layer", one frame per read/write). So `AcceptVfkit` is the correct
`virtualnetwork` accept method for a vz `FileHandleNetworkDeviceAttachment`
— confirmed, not guessed, by reading both sides' framing code.

**Note: this differs from how the real `vfkit`/`gvproxy` binaries connect.**
`crc-org/vfkit`'s `pkg/vf/virtionet.go` dials a *named* unixgram socket
(`net.DialUnix("unixgram", &localAddr, &remoteAddr)`) to a path `gvproxy`
listens on with `--listen-vfkit unixgram://path`, because vfkit and gvproxy
are separate OS processes. rask embeds gvisor-tap-vsock as a library in the
*same* process as the vz VM code, so an in-process `Socketpair` is simpler
and sidesteps macOS's ~104-byte unixgram path length limit entirely (no
socket file to create, chmod, or clean up).

**Host network config** (`github.com/containers/gvisor-tap-vsock/pkg/types.Configuration`):

```go
&types.Configuration{
    MTU:               1500,
    Subnet:            "192.168.127.0/24",
    GatewayIP:         "192.168.127.1",
    GatewayMacAddress: "5a:94:ef:e4:0c:dd",
    Forwards: map[string]string{
        "127.0.0.1:<hostPort>": "192.168.127.2:6443",
    },
}
```

`virtualnetwork.New(cfg)` binds the host TCP listener for every `Forwards`
entry *synchronously, inside `New()`*, before the VM even exists — confirmed
by reading `pkg/virtualnetwork/services.go`'s `forwardHostVM`, which calls
`forwarder.NewPortsForwarder(s).Expose(types.TCP, local, remote)` per entry,
which itself does a real `net.Listen`-backed `tcpproxy.Proxy` immediately.
So `<hostPort>` must be chosen (spike does bind-then-close-then-reuse on
`127.0.0.1:0`, a TOCTOU race acceptable for a single-VM-per-process design)
before calling `New()`.

**Guest network config** (`spikes/s4/init/main.go`, via
`github.com/vishvananda/netlink`, pure Go over `NETLINK_ROUTE` sockets — no
`ip`/busybox binary needed, matching S2's no-shell-in-initramfs approach):

```go
link, _ := netlink.LinkByName("eth0")
netlink.LinkSetUp(link)
addr, _ := netlink.ParseAddr("192.168.127.2/24")
netlink.AddrAdd(link, addr)
netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP("192.168.127.1")})
os.WriteFile("/etc/resolv.conf", []byte("nameserver 192.168.127.1\n"), 0o644)
```

The guest interface is `eth0` — confirmed empirically (no predictable
network interface naming without udev in this minimal environment), and the
static-IP path was used rather than DHCP (the task allowed either;
gvisor-tap-vsock's DHCP server is running regardless per
`pkg/virtualnetwork/services.go`'s unconditional `dhcpServer()` call, so
that path is available too but wasn't exercised here).

## Measurement methodology

- `t0` = `time.Now()` immediately before `vm.Start()`.
- `httpReady` = first successful `GET http://127.0.0.1:<hostPort>/` with the
  expected body (`rask-spike-s4-guest-ok\n`), polled every 5ms with a 200ms
  per-attempt client timeout, since `time.Now()`. This is the metric asked
  for: "boot -> first successful HTTP response through the forward".
- `netReady` = time the guest's `RASK-INIT-NET-READY` console marker
  arrives (informational; shown alongside `httpReady` to see how much of the
  budget is guest-side network bring-up vs. the forward path itself).
- The guest's outbound probe (`RASK-OUTBOUND-RESULT status=<n>
  attempt=<n>`, from a `net/http` client `GET http://example.com` with a
  3s-per-attempt timeout, retried up to 5 times) is only awaited on run 1 —
  it answers a yes/no question about the substrate, not a per-boot latency,
  so paying its cost on every one of 10 runs would be wasted wall time.
- Each run creates a brand-new `virtualnetwork.VirtualNetwork` and a
  brand-new `vz.VirtualMachine` (cold objects per run, matching S2's
  methodology); the VM is force-stopped and the socketpair closed before the
  next iteration.
- Command: `./work/spike-s4 --runs 10 --http-timeout 5s --outbound-timeout 15s`

## Results (10 runs)

```
run 01: netReady=137.233708ms httpReady=138.097875ms
  outbound probe: RASK-OUTBOUND-RESULT status=200 attempt=1
run 02: netReady=125.956166ms httpReady=126.765666ms
run 03: netReady=141.410083ms httpReady=142.216416ms
run 04: netReady=139.666041ms httpReady=140.425458ms
run 05: netReady=140.729042ms httpReady=141.414417ms
run 06: netReady=139.276875ms httpReady=140.343208ms
run 07: netReady=130.307375ms httpReady=130.963042ms
run 08: netReady=142.217209ms httpReady=142.883209ms
run 09: netReady=139.685ms   httpReady=140.391583ms
run 10: netReady=138.413666ms httpReady=139.060083ms

httpReady: p50=140.343208ms p95=142.883209ms min=126.765666ms max=142.883209ms (n=10)
netReady:  p50=139.276875ms p95=142.217209ms min=125.956166ms max=142.217209ms (n=10)
outbound NAT/DNS probe (run 1): RASK-OUTBOUND-RESULT status=200 attempt=1
```

`httpReady` and `netReady` are within ~0.6-1.1ms of each other on every
run, the same pattern S2 saw between its `t1`/`t2` checkpoints: once the
guest's network stack is up and the marker fires, the HTTP round-trip
through the host forward is essentially free relative to boot itself. An
earlier interactive single-run check (`-v`, verbose console echo) also
completed successfully end-to-end (`netReady=145.9ms httpReady=146.8ms`,
`outbound status=200 attempt=1`), confirming this isn't a fluke of the
specific 10-run batch.

## Outbound NAT / DNS

**Works, first attempt, both times checked** (the verbose single-run check
and run 1 of the 10-run batch). No `NAT`/`GatewayVirtualIPs` config entries
were needed for plain outbound egress — reading
`pkg/services/forwarder/tcp.go` confirms the general TCP forwarder handles
*any* TCP SYN reaching the gvisor stack's NIC by doing a real
`net.Dial("tcp", ...)` from the host process itself and splicing the
connection; this is genuine userspace NAT, not iptables, so it transparently
inherits whatever network path the host Mac has (corporate VPN, custom
resolvers, etc.) — relevant for rask's later image-pull work. DNS resolved
correctly too: the guest's `/etc/resolv.conf` points at
`192.168.127.1:53`, which `pkg/services/dns` serves via an upstream
resolver fallback for names outside the configured `Zone` records (we
configured none), so `example.com` resolved through the host's own DNS
without any extra wiring.

## Memory footprint

Host `spike-s4` process across the full 10-run batch (`/usr/bin/time -l`):
maximum resident set size 37.9MB (36.2MiB), peak memory footprint 21.6MB.
Higher than S2's pure-boot-only number (16.0MB RSS) because this process
also runs gvisor-tap-vsock's userspace network stack (gVisor `tcpip.Stack`,
DNS server, DHCP server, TCP/UDP forwarders) — this is a per-*process* cost,
not per-VM, and in rask's real architecture (one `virtualnetwork` created
once per cluster VM, not recreated per boot as this spike does across 10
runs) it's paid once at cluster-create time.

## gvisor-tap-vsock / vz API surprises and version-pinning gotchas

- **`vn.AcceptVfkit(ctx, conn)` does not unblock on `ctx` cancellation
  alone.** `Switch.rxNonStream` checks `ctx.Done()` only between reads; if
  it's parked in `conn.Read()`, cancelling the context does nothing until
  the read returns. The only reliable way to stop it is closing the
  connection, which surfaces as a benign `"cannot receive packets from ,
  disconnecting: ... use of closed network connection"` error logged via
  gvisor-tap-vsock's package-level `logrus` logger. The spike calls
  `logrus.SetLevel(logrus.FatalLevel)` in `main()` to keep 10-run output
  readable; production code should expect and ignore exactly this error
  string on intentional teardown, not treat it as a real fault.
- **`virtualnetwork.VirtualNetwork` has no `Close()`/`Shutdown()`.** Once
  created, its forwarder host listeners, DNS server (port 53), and DHCP
  server run for the life of the process. This is a non-issue for rask's
  actual design (one `VirtualNetwork` per cluster VM, created once, process
  exits on cluster delete) but means this spike leaks a forwarder listener
  + goroutine set per run across its own 10-run loop — deliberate spike
  shortcut, not a bug to fix here, but worth flagging so nobody copies the
  "recreate `virtualnetwork.New()` per boot" pattern into a long-lived
  multi-cluster daemon (v2 warm pool) without adding real per-VM network
  stack lifecycle management.
- **`configuration.Forwards` binds synchronously inside `New()`, before the
  guest exists.** A production caller must pick the host port (and, if
  concurrent cluster creates are ever a thing, actually reserve it — the
  spike's bind-then-close-then-`New()` sequence has a TOCTOU window) before
  calling `virtualnetwork.New()`.
- **No extra macOS entitlement needed.** Only S2's existing
  `com.apple.security.virtualization` entitlement was required. vz's
  `BridgedNetworkDeviceAttachment` needs `com.apple.vm.networking` per its
  own doc comment (confirmed by reading `network.go`), but
  `FileHandleNetworkDeviceAttachment` — the one this recipe uses — does not.
- **Version pins**: `github.com/Code-Hex/vz/v3` v3.7.1 (identical pin to
  S2 — no conflict with adding gvisor-tap-vsock, which is pure Go/no cgo).
  `github.com/containers/gvisor-tap-vsock` v0.8.9 pulls
  `gvisor.dev/gvisor` as an unreleased pseudo-version
  (`v0.0.0-20240916094835-a174eb65023f` — that module doesn't publish
  semver tags), which `go mod tidy` resolved cleanly; nothing further to
  pin manually. `github.com/vishvananda/netlink` v1.3.1 builds fine
  `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` (pure Go over `AF_NETLINK`
  sockets), consistent with S2's no-libc, no-shell initramfs approach — the
  static `rask-init` binary grew from S2's ~1.7MB to ~1.7MB* (net/http +
  netlink pulled in) and the cpio initramfs from S2's 1.7MB to 5.8MB
  accordingly (still trivially fast to page in at boot; no measurable
  latency regression was observed).

\* both are statically linked single binaries; the initramfs size delta is
almost entirely the added `net/http` server/client and `netlink` packages.

## Threat assessment for rask's macOS substrate

Nothing observed here threatens the vz + gvisor-tap-vsock design from
`research-m0-spikes.md`:

- The forward path adds ~15ms over S2's pure-boot p50 (125ms -> 140ms) —
  negligible against both the 2s spike target and k3d's 7-16s full-cluster
  numbers. Whatever dominates rask's end-to-end create time will still be
  control-plane bootstrap (S1's concern), not this networking layer.
- Outbound NAT and DNS work with zero extra configuration beyond the
  `Forwards` entry needed for the inbound apiserver path — this de-risks
  the later image-pull work (S3, M1) that depends on the same NAT path for
  `containerd` pulling from registries.
- The one real process-level risk worth carrying into production code
  (`internal/substrate/vz` + `internal/network`, per `plan-m0-spikes.md`'s
  M1 scaffolding) is the `AcceptVfkit` shutdown behavior: it must be paired
  with an explicit "close the connection" teardown step, not a bare
  `context.CancelFunc`, or a real cluster's network goroutine will hang on
  `vm delete`.
