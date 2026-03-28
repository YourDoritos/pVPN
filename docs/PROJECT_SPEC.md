# pVPN - Proton VPN Client for Linux

> "A project is only as good as the plan" - Sun Tzu (probably)

## Overview

A full-featured Proton VPN client written in Go with a TUI interface, designed for
**any Linux system** — works with both **NetworkManager** and **systemd-networkd + iwd**.
Ships as a single static binary with zero runtime dependencies.
Implements all Proton VPN features including Stealth protocol for DPI bypass.

**Why this exists:** The official Proton VPN Linux clients (`proton-vpn-cli`,
`proton-vpn-gtk-app`) hard-depend on NetworkManager via `libnm` D-Bus bindings.
They do not work on systemd-networkd setups. Additionally, the Stealth protocol
(WireGuard-over-TLS) is not available on Linux at all -- not even on the official
clients. pVPN fixes both problems and works regardless of your network stack.

## Design Philosophy: Zero-Config UX

**The user should never need to configure anything outside of pVPN itself.**

- **Install:** One command (`curl -sL .../install.sh | sudo bash` or AUR/distro package)
- **Run:** `pvpn` (auto-elevates with polkit if needed, or run with `sudo`)
- **Use:** TUI guides through login, server selection, and connection. No config files
  to edit, no manual WireGuard setup, no DNS scripts, no firewall rules to write.

Everything is handled internally:
- Auto-detects network backend (NetworkManager vs systemd-networkd vs direct)
- Auto-creates WireGuard interfaces
- Auto-configures DNS (via the detected backend)
- Auto-manages kill switch rules
- Auto-rotates certificates
- Auto-reconnects on network changes
- Session persists across restarts (no re-login)

If the user wants to tweak settings, they do it in the TUI settings screen — never
by editing files or running commands.

---

## Architecture

```
+------------------+     +------------------+     +-------------------+
|                  |     |                  |     |                   |
|   TUI Frontend   |---->|   Core Engine    |---->|   Proton API      |
|   (bubbletea)    |     |                  |     |   (go-proton-api) |
|                  |     |   - Connection   |     |                   |
+------------------+     |   - Kill Switch  |     +-------------------+
                         |   - Cert Refresh |
                         |                  |     +-------------------+
                         |                  |---->|   WireGuard       |
                         +------------------+     |   (wgctrl/netlink)|
                                |                 +-------------------+
                                |
                                v
                         +------------------+     +-------------------+
                         |  Network Backend |---->| NetworkManager    |
                         |  (auto-detect)   |     | (D-Bus)           |
                         |                  |     +-------------------+
                         |  - DNS provider  |
                         |  - Route mgmt    |     +-------------------+
                         |                  |---->| systemd-resolved  |
                         +------------------+     | systemd-networkd  |
                                |                 | (D-Bus)           |
                                v                 +-------------------+
                         +------------------+
                         | Stealth Layer    |
                         | (TLS obfuscation)|
                         +------------------+
```

### Component Breakdown

| Component | Responsibility |
|-----------|---------------|
| **TUI (pvpn)** | User interface, server browser, connection status, settings. Communicates with daemon via IPC. |
| **CLI (pvpnctl)** | Scriptable command-line interface. Same IPC protocol as TUI. |
| **Daemon (pvpnd)** | Privileged background service. Owns VPN lifecycle, runs as systemd service. (Phase 3) |
| **Core Engine** | Orchestrates connections, manages state machine, background tasks |
| **Proton API** | Auth (SRP+2FA), server list, certificate management, session refresh |
| **WireGuard Manager** | Interface creation/teardown via kernel netlink |
| **Stealth Layer** | WireGuard-over-TLS with TunSafe framing (ported from ProtonVPN/wireguard-go) |
| **Network Backend** | Auto-detects NM vs systemd-networkd; manages DNS + routes via the right backend |
| **Kill Switch** | nftables rules (works identically on all backends) |
| **Cert Daemon** | Background certificate rotation with randomized intervals |

> **Note:** Phase 1-2 use a single-process model (TUI embeds the engine).
> Phase 3 splits this into daemon + client. The core engine code stays the same —
> it just moves from being called directly by the TUI to being called by the daemon,
> with the TUI becoming a thin IPC client.

---

## Dependencies (What We Borrow)

### Direct Dependencies (import as Go modules)

| Library | License | Purpose | Notes |
|---------|---------|---------|-------|
| `ProtonMail/go-proton-api` | MIT | Auth, SRP, session management | Foundation for all API calls |
| `ProtonMail/go-srp` | MIT | SRP protocol | Pulled in by go-proton-api |
| `ProtonVPN/go-vpn-lib/ed25519` | GPL-3.0 | Ed25519 key gen, X25519 conversion | Required for WG key exchange |
| `ProtonVPN/go-vpn-lib/localAgent` | GPL-3.0 | Post-connection feature negotiation | NetShield, port forwarding, etc. |
| `WireGuard/wgctrl-go` | MIT | WireGuard interface configuration via netlink | No NM needed |
| `vishvananda/netlink` | Apache-2.0 | Network interface, route, rule management | Replaces `ip` commands |
| `refraction-networking/utls` | BSD-3 | TLS fingerprint mimicry | Required for Stealth |
| `golang.zx2c4.com/wireguard` | MIT | WireGuard userspace implementation | Required for Stealth mode (kernel WG can't intercept packet path) |
| `godbus/dbus` | BSD-2 | D-Bus protocol | Used by go-systemd; also used directly for NM D-Bus API |
| `coreos/go-systemd` | Apache-2.0 | systemd D-Bus (resolved, networkd) | DNS + reload |
| `golang.org/x/crypto` | BSD-3 | nacl/secretbox + argon2 | Session file encryption (key derived from machine-id) |
| `charmbracelet/bubbletea` | MIT | TUI framework | Elm architecture |
| `charmbracelet/lipgloss` | MIT | TUI styling | Borders, colors, layout |
| `charmbracelet/bubbles` | MIT | TUI components | Lists, spinners, tables, inputs |

### Reference Code (study, adapt, rewrite into our codebase)

| Source | What We Take |
|--------|-------------|
| `hatemosphere/protonvpn-wg-confgen` (GPL-3.0) | Proven Go auth flow, API types, server list filtering/scoring. Closest existing Go implementation of Proton VPN API calls. |
| `ProtonVPN/wireguard-go` fork (GPL-3.0) | Stealth protocol: `conn/bind_std_tcp.go` (TLS bind), `conn/tcp_tls_utils.go` (TunSafe framing). This IS the Stealth reference implementation. |
| `ProtonVPN/python-proton-vpn-api-core` (GPL-3.0) | Definitive API surface docs: all endpoints, request/response schemas, cert refresh logic, feature flags. |

### License Consequence

`go-vpn-lib` is GPL-3.0 -> **pVPN must be GPL-3.0**. All other deps are GPL-compatible.

---

## Proton API Surface

### Authentication

```
POST /auth/v4/info     {Username}              -> SRP params (Modulus, Salt, ServerEphemeral, SRPSession)
POST /auth/v4          {Username, ClientProof,  -> {AccessToken, RefreshToken, UID, ServerProof, 2FA}
                        ClientEphemeral,
                        SRPSession}
POST /auth/v4/2fa      {TwoFactorCode}         -> confirm 2FA
```

All subsequent requests carry:
- `Authorization: Bearer <AccessToken>`
- `x-pm-uid: <UID>`
- `x-pm-appversion: LinuxVPN_x.y.z`

Token refresh happens automatically via `go-proton-api`'s refresh mechanism using the RefreshToken.

### VPN Endpoints

```
GET  /vpn/v2              -> account info (plan, max connections, tier)
GET  /vpn/v1/logicals     -> server list (name, country, features, load, score, X25519 pubkey, servers[])
GET  /vpn/v1/clientconfig -> client config (ports, feature flags, smart protocol settings)
GET  /vpn/v1/location     -> client's current IP/country/ISP
GET  /vpn/v1/sessions     -> active VPN sessions
POST /vpn/v1/certificate  -> WireGuard certificate
     {ClientPublicKey (Ed25519 PEM), Duration, Features{}, DeviceName, Mode}
```

### Key Exchange Flow

```
1. Generate Ed25519 keypair
2. Convert to X25519 -> this is the WireGuard private key
3. POST Ed25519 public key (PEM) to /vpn/v1/certificate
4. Receive X.509 certificate (for Local Agent TLS mutual auth)
5. Build WireGuard config:
   - PrivateKey = X25519 private key (from step 2)
   - PublicKey = server's X25519PublicKey (from /vpn/v1/logicals)
   - Endpoint = server IP:port
```

### Certificate Features (sent in cert request)

| Feature | Values | Description |
|---------|--------|-------------|
| `NetShieldLevel` | 0, 1, 2 | Off / Malware block / Malware+Ads+Trackers |
| `SplitTCP` | bool | VPN Accelerator |
| `RandomNAT` | bool | false = Moderate NAT |
| `PortForwarding` | bool | Enable port forwarding |

### Certificate Rotation

- Default duration: 7 days (configurable)
- Refresh interval: randomized +/-22% of remaining validity
- Min validity before forced refresh: 300 seconds
- Background goroutine handles this automatically

### Server Feature Bitmask

```
1  = Secure Core
2  = Tor
4  = P2P
8  = Streaming
16 = IPv6
```

### Server Tiers

```
0 = Free
1 = Basic (legacy, treat as Plus)
2 = Plus
3 = Visionary
```

### Local Agent Protocol (Post-Connection)

After WireGuard tunnel is up, connect via TLS to server port 65215 for feature
negotiation. Uses mutual TLS (mTLS):
- **Client cert**: The X.509 certificate from `POST /vpn/v1/certificate`
- **Client key**: The Ed25519 private key used to request the certificate
- **Server CA**: The `RootCerts` from `GET /vpn/v1/clientconfig` (PEM-encoded, pinned)
- **Server name verification**: Uses the server's entry IP as the expected name

Binary framing: 4-byte big-endian length prefix + JSON message.

Messages: `status`, `features-set`, `status-get`, `error`
States: `Connecting`, `Connected`, `SoftJailed`, `HardJailed`, `ConnectionError`, `ServerUnreachable`

This is how features (NetShield, port forwarding, etc.) are negotiated/changed
at runtime without reconnecting.

---

## Stealth Protocol (WireGuard-over-TLS)

This is the key differentiator. Proton's Stealth wraps WireGuard in TLS so it
looks like regular HTTPS to DPI/firewalls.

### How It Works (from Proton's wireguard-go fork)

1. **TLS Connection**: Connect to server on TCP port 443 using `utls` with a
   Chrome TLS fingerprint (`HelloChrome_Auto`, `HelloChrome_120_PQ`, etc.)
2. **TunSafe Framing**: WireGuard packets are wrapped in a 2-byte header:
   - Byte 0: type (0x00 = normal WG packet, 0x01 = data packet with stripped header)
   - Byte 1: size high byte (combined with next byte for 16-bit length)
   - Data packets strip the WireGuard header to save bandwidth
3. **Result**: Traffic on the wire is TLS on port 443, indistinguishable from HTTPS

### Implementation Plan

Port the following from `ProtonVPN/wireguard-go`:
- `conn/bind_std_tcp.go` -> our `internal/stealth/tlsbind.go`
- `conn/tcp_tls_utils.go` -> our `internal/stealth/framing.go`
- `conn/server_name_utils/` -> our `internal/stealth/sni.go`

For Stealth mode, we use `wireguard-go` userspace (not kernel WireGuard) since
we need to intercept the packet send/receive path to route through TLS.
For normal WireGuard mode, we use the kernel module via `wgctrl-go` (faster).

---

## Features

### MVP (Phase 1)

- [x] SRP authentication with 2FA support
- [x] Session persistence and automatic token refresh
- [x] Server list fetching with country/feature/load filtering
- [x] WireGuard connection via kernel module (wgctrl + netlink)
- [x] DNS management via auto-detected backend (NM / systemd-resolved / direct)
- [x] Basic kill switch (nftables rules: block all non-VPN traffic)
- [x] Certificate generation and automatic rotation
- [x] Local Agent connection for feature negotiation
- [x] TUI: login screen, server browser, connection status
- [x] Config file (TOML) for persistent settings

### Phase 2

- [x] Stealth protocol (WireGuard-over-TLS)
- [x] Smart protocol selection (auto-detect best protocol/port)
- [x] NetShield (ad/tracker/malware blocking via cert features)
- [x] VPN Accelerator (SplitTCP)
- [x] Moderate NAT
- [x] Port forwarding
- [x] Server load-based auto-selection (fastest server)
- [x] Reconnection logic with exponential backoff
- [x] TUI: animations, search, favorites, connection history

### Phase 3

- [ ] Split tunneling (cgroup/fwmark-based routing)
- [x] Secure Core (multi-hop via entry -> exit servers)
- [x] Custom DNS servers
- [ ] IPv6 support
- [x] Tor server support (filter toggle in server browser)
- [x] Streaming-optimized server selection (filter toggle in server browser)
- [x] Port forwarding status display (NAT-PMP with auto-renewal)
- [ ] System tray integration (optional)
- [ ] Automatic updates check

---

## Project Structure

```
pVPN/
+-- cmd/
|   +-- pvpn/
|   |   +-- main.go              # TUI client entry point
|   +-- pvpnd/
|   |   +-- main.go              # Daemon entry point
|   +-- pvpnctl/
|   |   +-- main.go              # CLI client entry point
|   +-- conntest/
|       +-- main.go              # Connection test utility
+-- internal/
|   +-- api/
|   |   +-- client.go            # Proton API client (wraps go-proton-api)
|   |   +-- auth.go              # Auth flow (SRP + 2FA)
|   |   +-- servers.go           # Server list, filtering, scoring
|   |   +-- certificate.go       # Cert generation and rotation
|   |   +-- session.go           # Session persistence, token refresh
|   |   +-- types.go             # API request/response types
|   +-- vpn/
|   |   +-- connection.go        # Connection state machine
|   |   +-- wireguard.go         # WireGuard interface management (wgctrl + netlink)
|   |   +-- localagent.go        # Local Agent protocol wrapper
|   |   +-- killswitch.go        # nftables kill switch
|   |   +-- routes.go            # Route management
|   |   +-- cleanup.go           # Safety-net cleanup on exit
|   |   +-- portforward.go       # NAT-PMP port forwarding with auto-renewal
|   +-- network/
|   |   +-- detect.go            # Auto-detect active network backend
|   |   +-- backend.go           # Backend interface (DNS set/revert, route hints)
|   |   +-- nm.go                # NetworkManager D-Bus backend
|   |   +-- resolved.go          # systemd-resolved D-Bus backend
|   |   +-- direct.go            # Fallback: direct /etc/resolv.conf manipulation
|   +-- stealth/
|   |   +-- tlsbind.go           # WireGuard-over-TLS bind (ported from Proton)
|   |   +-- framing.go           # TunSafe framing protocol
|   |   +-- stealth.go           # Stealth tunnel manager (wireguard-go userspace)
|   +-- daemon/
|   |   +-- daemon.go            # Daemon lifecycle, signal handling
|   |   +-- ipc.go               # Unix socket IPC server
|   |   +-- commands.go          # Command handlers (connect, disconnect, status, ...)
|   +-- ipc/
|   |   +-- client.go            # IPC client (used by TUI and pvpnctl)
|   |   +-- protocol.go          # Wire protocol: JSON over Unix socket
|   +-- tui/
|   |   +-- app.go               # Bubbletea app root
|   |   +-- login.go             # Login screen
|   |   +-- servers.go           # Server browser (list, search, filter)
|   |   +-- status.go            # Connection status dashboard
|   |   +-- settings.go          # Settings/preferences
|   |   +-- messages.go          # TUI message types
|   |   +-- theme.go             # Colors, styles (Proton brand colors)
|   +-- config/
|       +-- config.go            # TOML config file management
|       +-- paths.go             # XDG paths (~/.config/pvpn, ~/.local/share/pvpn)
+-- dist/
|   +-- pvpnd.service            # systemd service file
|   +-- pvpn-waybar.sh           # Waybar integration script
|   +-- PKGBUILD                 # AUR package definition
|   +-- pvpn.install             # AUR post-install hooks
+-- docs/
|   +-- PROJECT_SPEC.md          # This file
+-- Makefile                     # Build, install, uninstall targets (DESTDIR/PREFIX support)
+-- install.sh                   # Standalone install script
+-- uninstall.sh                 # Standalone uninstall script
+-- .gitignore
+-- go.mod
+-- go.sum
+-- LICENSE                      # GPL-3.0
+-- README.md
```

---

## Network Stack Integration

### Backend Auto-Detection

On startup, pVPN detects which network stack is active:

```
1. Check if NetworkManager is running (D-Bus: org.freedesktop.NetworkManager)
   -> Use NM backend (DNS via NM D-Bus)
2. Check if systemd-resolved is running (D-Bus: org.freedesktop.resolve1)
   -> Use systemd-resolved backend (DNS via resolved D-Bus)
3. Fallback: direct /etc/resolv.conf manipulation
   -> Last resort, works everywhere
```

The user never needs to configure this. WireGuard interface creation, routes, and
kill switch work identically regardless of backend — only DNS management differs.

### Common Layer (all backends)

| Operation | Implementation |
|-----------|---------------|
| Create WG interface | `netlink.LinkAdd(&netlink.Wireguard{})` |
| Set WG private key + peers | `wgctrl.Client.ConfigureDevice()` |
| Assign IP address | `netlink.AddrAdd()` |
| Add routes | `netlink.RouteAdd()` with fwmark for policy routing |
| Kill switch | `nftables` rules: default drop, allow VPN + LAN |
| Teardown | Reverse all of the above |

### DNS Backend: NetworkManager

Via D-Bus (`org.freedesktop.NetworkManager`):
- Add a NM unmanaged device for `pvpn0` (so NM doesn't fight us for the interface)
- `org.freedesktop.NetworkManager.DnsManager` to push DNS config
- Alternatively: set DNS on the connection profile via `Update2()`
- On disconnect: NM automatically restores DNS when the interface goes away

### DNS Backend: systemd-resolved

Via D-Bus (`org.freedesktop.resolve1`):
- `SetLinkDNS(ifindex, [[AF_INET, ip]])` - set DNS servers on VPN interface
- `SetLinkDomains(ifindex, [[".", true]])` - route all DNS through VPN
- `SetLinkDefaultRoute(ifindex, true)` - make VPN the default DNS route
- On disconnect: `RevertLink(ifindex)` - restore original DNS

### DNS Backend: Direct (fallback)

- Backup `/etc/resolv.conf` to `/etc/resolv.conf.pvpn.bak`
- Write Proton DNS servers to `/etc/resolv.conf`
- On disconnect: restore from backup

### Kill Switch (nftables)

```
table inet pvpn_killswitch {
    chain output {
        type filter hook output priority 0; policy drop;
        oif "lo" accept                          # loopback
        oif "pvpn0" accept                       # VPN interface
        ip daddr <vpn_server_ip> accept          # VPN server
        ip daddr 10.0.0.0/8 accept               # LAN (configurable)
        ip daddr 172.16.0.0/12 accept
        ip daddr 192.168.0.0/16 accept
        udp dport { 67, 68 } accept              # DHCP lease renewal
        ip6 daddr fe80::/10 udp dport { 546, 547 } accept  # DHCPv6
        ct state established,related accept       # existing connections
        drop                                      # everything else
    }
}
```

### Interface Naming

- VPN interface: `pvpn0` (predictable, avoids conflicts)
- Routing table: custom table `51820` with fwmark for policy routing

---

## Configuration

Config file: `~/.config/pvpn/config.toml`

```toml
[account]
username = ""  # stored separately in keyring or encrypted

[connection]
protocol = "wireguard"       # "wireguard" | "stealth"
default_port = 51820
killswitch = true
auto_connect = false
reconnect = true

[features]
netshield = 0                # 0=off, 1=malware, 2=malware+ads+trackers
vpn_accelerator = true
moderate_nat = false
port_forwarding = false

[server]
default_country = ""
prefer_p2p = false
prefer_secure_core = false
last_server = ""

[dns]
custom_dns = []              # empty = use Proton DNS; e.g. ["1.1.1.1", "8.8.8.8"]
# Note: custom DNS bypasses NetShield ad/tracker/malware blocking
```

Session/credentials: `~/.local/share/pvpn/session.enc`
- Encrypted with `nacl/secretbox` (XSalsa20-Poly1305)
- Encryption key derived via Argon2id from `/etc/machine-id` (unique per install)
- Contains: AccessToken, RefreshToken, UID, Ed25519 keypair
- Auto-created on first login, auto-refreshed on token rotation
- If machine-id changes or file is corrupted, user is prompted to re-login (not an error, just a fresh start)

---

## Known Limitations & Risks

| Risk | Mitigation |
|------|-----------|
| Proton API is undocumented/private | We reference their open-source Python client for the API surface. API could change. |
| Stealth protocol is reverse-engineered from their Go fork | Their wireguard-go fork is GPL-3.0 and public. We're allowed to use it. |
| Requires root/CAP_NET_ADMIN+CAP_NET_RAW for WG + nftables | Phase 1-2: run as root. Phase 3: daemon runs with capabilities, clients are unprivileged. |
| Stealth VPN dies when TUI closes (userspace wireguard-go) | Phase 3 daemon architecture solves this — daemon owns the tunnel, TUI is just a controller. |
| go-proton-api is mail-focused | We wrap its HTTP client (not fork) and add VPN endpoints as methods on our own API client struct. Auth layer is shared. Upstream updates stay easy. |
| Certificate pinning / API versioning | Monitor Proton's repos for breaking changes. |
| Proton could block third-party clients | Unlikely given their open-source stance, but possible. Use their official app-version strings. |

---

## Development Phases & Milestones

### Phase 1: MVP (~4-6 weeks of focused work)

**Milestone 1.1: API & Auth**
- Proton API client with SRP auth + 2FA
- Session persistence
- Server list fetching

**Milestone 1.2: WireGuard Connection**
- Interface creation/teardown via netlink
- WireGuard config via wgctrl
- DNS via systemd-resolved
- Basic kill switch

**Milestone 1.3: Certificate Management**
- Key generation (Ed25519 -> X25519)
- Certificate requests and rotation
- Local Agent connection

**Milestone 1.4: TUI**
- Login flow
- Server browser with filtering
- Connection status
- Settings screen

### Phase 2: Full Feature Parity

**Milestone 2.1: Stealth Protocol**
- Port TLS bind from Proton's wireguard-go
- uTLS fingerprinting
- Smart protocol selection

**Milestone 2.2: Advanced Features**
- NetShield, VPN Accelerator, Moderate NAT, Port Forwarding
- Reconnection with backoff
- Server scoring and auto-selection

### Phase 3: Daemon Architecture & Power User Features

**Milestone 3.1: Daemon/Client Split (REQUIRED)**

The single-process TUI architecture has a fundamental limitation: the VPN
dies when the TUI closes (especially with Stealth, which uses userspace
wireguard-go). Phase 3 rearchitects pVPN into a privileged daemon + unprivileged
client model. This is the foundation for auto-connect, boot persistence, and
system tray integration.

Architecture:
```
+------------------+         +------------------+
|                  |  Unix   |                  |
|   pvpn (TUI)     |-------->|   pvpnd (daemon) |
|   (unprivileged) | Socket  |   (root/caps)    |
|                  |  IPC    |                  |
+------------------+         |   - Connection   |
                              |   - Kill Switch  |
+------------------+         |   - Cert Refresh |
|                  |-------->|   - Reconnection |
|   pvpnctl (CLI)  |  IPC    |   - DNS mgmt    |
|   (unprivileged) |         |   - WG/Stealth   |
|                  |         |                  |
+------------------+         +------------------+
                                      |
+------------------+                  v
|  Waybar module   |<-------- Status socket/file
|  (status only)   |
+------------------+
```

Components:
- **pvpnd**: Privileged daemon (systemd service). Owns the VPN connection,
  WireGuard interface, routes, DNS, kill switch, certificate rotation, and
  reconnection. Listens on a Unix domain socket for commands.
  Runs as: `systemd service` or `pvpnd --foreground`
- **pvpn**: TUI client (unprivileged). Connects to daemon via Unix socket.
  Sends commands (connect, disconnect, status, settings). Displays state.
  Can be opened/closed freely without affecting the VPN.
- **pvpnctl**: CLI client (unprivileged). Same IPC protocol, no TUI.
  For scripting: `pvpnctl connect NL#42`, `pvpnctl status`, `pvpnctl disconnect`
- **Waybar module**: Reads status from daemon (connected server, protocol,
  traffic stats) and displays in the status bar.

IPC Protocol (Unix socket at `/run/pvpn/pvpn.sock`):
```
Commands (client → daemon):
  login <username> <password> [2fa]
  connect <server-name|country|"fastest"> [--protocol smart|wg|stealth]
  disconnect
  status                    → JSON: {state, server, ip, country, duration, rx, tx, protocol}
  servers [--country XX]    → JSON: [{name, country, load, features, ...}]
  settings get              → JSON: current config
  settings set <key> <val>  → update config
  reconnect

Events (daemon → client, streamed):
  state-changed {state, ...}
  stats-update {rx, tx, handshake}
  log <message>
```

Systemd integration:
```ini
# /etc/systemd/system/pvpnd.service
[Unit]
Description=pVPN Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/bin/pvpnd
Restart=on-failure
RestartSec=5
RuntimeDirectory=pvpn
StateDirectory=pvpn

# Security hardening
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/run/pvpn /var/lib/pvpn /etc/resolv.conf

[Install]
WantedBy=multi-user.target
```

Auto-connect on boot:
- Config flag `auto_connect = true` + `last_server = "NL#42"`
- pvpnd reads config on startup, connects automatically if enabled
- Reconnection logic (from Phase 2) handles network changes after boot

Waybar integration:
```json
// ~/.config/waybar/config
{
    "custom/pvpn": {
        "exec": "pvpnctl status --format waybar",
        "return-type": "json",
        "interval": 5,
        "on-click": "pvpn"
    }
}
```

Migration path:
1. Extract core engine from cmd/pvpn into internal/daemon/
2. Add Unix socket IPC server to daemon
3. Rewrite TUI to be a thin IPC client
4. Add pvpnctl CLI
5. Add systemd service file
6. Add waybar output format

**Milestone 3.2: Power User Features**
- Split tunneling (cgroup/fwmark-based routing)
- Secure Core (multi-hop via entry → exit servers)
- Custom DNS servers
- IPv6 support
- Tor server support
- Streaming-optimized server selection
- Port forwarding status display
- Automatic updates check

---

## Testing Strategy

| Level | Approach |
|-------|----------|
| **Unit** | Mock Proton API responses, test server filtering/scoring, config parsing |
| **Integration** | Test against Proton API with a real account (requires Plus subscription for Stealth) |
| **Network** | Test WG interface creation, route management, DNS, kill switch in a network namespace |
| **E2E** | Full connection test: auth -> connect -> verify IP changed -> disconnect |
| **Stealth** | Test in a restricted network environment (firewall blocking UDP/WG) |

---

## Build & Run

### Phase 1-2 (single process, current)

```bash
# Build (static binary, no runtime deps)
CGO_ENABLED=0 go build -o pvpn ./cmd/pvpn

# Run (requires root for WireGuard + nftables)
sudo ./pvpn
```

### Phase 3 (daemon + client)

```bash
# Build all binaries
make build

# Install (builds + installs binaries + systemd service)
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now pvpnd

# Uninstall
sudo make uninstall
```

Usage:
```bash
pvpn                          # Open TUI (connects to daemon)
pvpnctl connect NL#42         # Connect via CLI
pvpnctl status                # Check status
pvpnctl disconnect            # Disconnect
```

### Installation (end-user)

```bash
# Option 1: AUR (Arch Linux)
yay -S pvpn
sudo systemctl daemon-reload
sudo systemctl enable --now pvpnd

# Option 2: From source
git clone https://github.com/YourDoritos/pvpn.git
cd pvpn
sudo make install
sudo systemctl daemon-reload
sudo systemctl enable --now pvpnd
```

After install, run `pvpn` for the TUI or `pvpnctl` for CLI usage.

---

## References

- [ProtonMail/go-proton-api](https://github.com/ProtonMail/go-proton-api) - Go Proton API client (MIT)
- [ProtonVPN/go-vpn-lib](https://github.com/ProtonVPN/go-vpn-lib) - VPN key management + Local Agent (GPL-3.0)
- [ProtonVPN/wireguard-go](https://github.com/ProtonVPN/wireguard-go) - Stealth protocol reference (GPL-3.0)
- [hatemosphere/protonvpn-wg-confgen](https://github.com/hatemosphere/protonvpn-wg-confgen) - Existing Go Proton VPN tool (GPL-3.0)
- [ProtonVPN/python-proton-vpn-api-core](https://github.com/ProtonVPN/python-proton-vpn-api-core) - Python API reference (GPL-3.0)
- [WireGuard/wgctrl-go](https://github.com/WireGuard/wgctrl-go) - WireGuard control (MIT)
- [vishvananda/netlink](https://github.com/vishvananda/netlink) - Linux netlink (Apache-2.0)
- [refraction-networking/utls](https://github.com/refraction-networking/utls) - uTLS fingerprinting (BSD-3)
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework (MIT)
- [coreos/go-systemd](https://github.com/coreos/go-systemd) - systemd integration (Apache-2.0)
