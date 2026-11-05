# whop forward 与 OpenSSH 端口转发对照

本文把 `whop forward` 的本地、远程、动态端口转发与 OpenSSH 的 `ssh -L`、`ssh -R`、`ssh -D` 放在一起对照，便于从 OpenSSH 命令迁移。

## 1. 总览

| 模式 | OpenSSH 等价命令 | whop 命令 | 监听端口在哪里 | 最终目标由谁连接 |
|---|---|---|---|---|
| 本地转发 | `ssh -N -L spec host` | `whop forward -L spec alias` | 本机 | 远端 sshd |
| 远程转发 | `ssh -N -R spec host` | `whop forward -R spec alias` | 远端 | 本机 whop |
| 动态转发 | `ssh -N -D spec host` | `whop forward -D spec alias` | 本机 | 远端 sshd，按 SOCKS5 请求选择目标 |

三者的共同点是：先建立一条正常 SSH 连接，然后复用这条连接传输额外的 TCP 数据。`whop forward` 不会启动远端 shell，因此语义上相当于 OpenSSH 的 `ssh -N`。

## 2. 一句话记忆

```text
-L：本机监听，固定一个远端目标
-R：远端监听，固定一个本地目标
-D：本机监听，目标由每次 SOCKS5 请求决定
```

这里的“本地”和“远端”都以运行 whop 的机器为参照：

- 本地：你运行 `whop forward` 的电脑。
- 远端：`alias` 对应的 SSH 服务器，以及从该服务器能访问到的网络。
- `-L` / `-D` 的 destination 由远端 sshd 解析和连接。
- `-R` 的 destination 由本机 whop 解析和连接。

## 3. 命令语法

```bash
whop forward [flags] <alias>
```

按需重复添加规则：

```text
-L [bind:]port:host:port
-R [bind:]port:host:port
-D [bind:]port
```

示例：

```bash
whop forward -L 127.0.0.1:8080:db.internal:5432 prod-db
whop forward -R 127.0.0.1:9000:localhost:3000 prod-db
whop forward -D 127.0.0.1:1080 prod-db
```

要点：

- `alias` 必须能从 `~/.ssh/config` 解析。
- HostName、User、Port、IdentityFile、ProxyJump 等沿用 SSH config。
- bind 省略时默认是 `127.0.0.1`。
- 本地和远端转发必须同时提供目标主机和目标端口。
- 动态转发不能提供固定 destination。
- IPv6 地址需要使用方括号，例如 `[::1]:8080`。
- CLI 可以一次给出多个 `-L`、`-R`、`-D`，它们会并发启动。
- TUI 表单一次配置并启动一个规则；退出转发后回到主机列表。

## 4. 与 OpenSSH 命令逐项对照

### 4.1 本地端口转发

OpenSSH：

```bash
ssh -N -L 127.0.0.1:15432:db.internal:5432 prod-db
```

whop：

```bash
whop forward -L 127.0.0.1:15432:db.internal:5432 prod-db
```

数据路径：

```text
本机程序
  -> 127.0.0.1:15432
  -> whop
  -> 已有 SSH 连接
  -> prod-db 的 sshd
  -> db.internal:5432
```

适用场景：

- 通过跳板机访问内网数据库
- 访问远端内网 Web 管理页面
- 本地没有 VPN，但有一台可 SSH 的入口机
- 临时替代 `ssh -L`

注意：

- `db.internal` 是从远端 sshd 所在网络解析和连接的。
- 本机监听端口可以与目标端口不同。
- 目标是否允许连接受 sshd 的 `AllowTcpForwarding` 和 `PermitOpen` 控制。

### 4.2 远程端口转发

OpenSSH：

```bash
ssh -N -R 127.0.0.1:9000:localhost:3000 prod-server
```

whop：

```bash
whop forward -R 127.0.0.1:9000:localhost:3000 prod-server
```

数据路径：

```text
远端程序
  -> prod-server 的 127.0.0.1:9000
  -> prod-server 的 sshd
  -> 已有 SSH 连接
  -> 本机 whop
  -> 本机 localhost:3000
```

适用场景：

- 远端环境回调本机的 webhook
- 远端服务访问本机开发服务器
- 本机在 NAT/防火墙后面，远端无法主动连接本机
- 临时把本机管理接口暴露给远端

注意：

- 这里的 `localhost:3000` 指本机，不是远端服务器。
- 默认绑定远端 loopback。绑定 `0.0.0.0` 可能受 sshd 的 `GatewayPorts` 限制。
- 远端 `PermitListen`、防火墙和端口占用都可能阻止监听。

### 4.3 动态 SOCKS5 转发

OpenSSH：

```bash
ssh -N -D 127.0.0.1:1080 prod-server
```

whop：

```bash
whop forward -D 127.0.0.1:1080 prod-server
```

数据路径：

```text
支持 SOCKS5 的应用
  -> 127.0.0.1:1080
  -> SOCKS5 CONNECT 目标地址
  -> whop
  -> 已有 SSH 连接
  -> prod-server 的 sshd
  -> 每次请求指定的目标
```

使用示例：

```bash
curl --socks5-hostname 127.0.0.1:1080 https://internal.example.com
```

浏览器配置：

```text
代理类型：SOCKS5
地址：127.0.0.1
端口：1080
```

适用场景：

- 用浏览器访问远端内网
- 同一个代理端口访问多个远端目标
- 不想为每个目标单独写一条 `-L`

注意：

- `-D` 不是透明端口转发；应用必须支持 SOCKS5。
- 使用 `--socks5-hostname` 时，域名由 sshd 一侧解析；使用本地解析模式时，应用可能先解析成 IP 再发送。
- whop 当前只实现 SOCKS5 TCP `CONNECT`，不支持 UDP ASSOCIATE，也没有 SOCKS5 用户名密码认证。
- 默认绑定 `127.0.0.1`。不要随意绑定 `0.0.0.0`，否则可能成为无认证代理入口。

## 5. 一次启动多条规则

OpenSSH 可以：

```bash
ssh -N \
  -L 127.0.0.1:15432:db.internal:5432 \
  -R 127.0.0.1:9000:localhost:3000 \
  -D 127.0.0.1:1080 \
  prod-db
```

whop 也可以：

```bash
whop forward \
  -L 127.0.0.1:15432:db.internal:5432 \
  -R 127.0.0.1:9000:localhost:3000 \
  -D 127.0.0.1:1080 \
  prod-db
```

`whop forward` 会保持前台运行，直到收到 `Ctrl+C`。它不会 daemonize，也不会自动重连。

## 6. TUI 字段对照

在主列表选中主机后按 `p`：

| 字段 | 填写内容 |
|---|---|
| Type | `local`、`remote` 或 `dynamic` |
| Bind | 监听侧地址，例如 `127.0.0.1:8080` |
| Destination | 固定目标，例如 `db.internal:5432`；dynamic 留空 |

对应关系：

```text
local   -> ssh -L
remote  -> ssh -R
dynamic -> ssh -D
```

提交是 `ctrl+s`，取消是 `esc`。启动后按 `Ctrl+C` 停止并返回 whop。

## 7. 与 OpenSSH 的差异和当前边界

已经对齐的能力：

- `-L [bind:]port:host:port`
- `-R [bind:]port:host:port`
- `-D [bind:]port`
- 多个转发规则同时启动
- 复用 SSH config 中的 HostName、User、Port、IdentityFile、ProxyJump
- 可选 ssh-agent 行为与普通 `whop connect` 一致
- 前台运行、`Ctrl+C` 退出

当前没有提供：

- `-f` 后台运行
- `-g` 风格选项；需要对外绑定时直接写 `0.0.0.0`
- `-o` 任意 OpenSSH 配置覆盖
- `ExitOnForwardFailure`、`ClearAllForwardings` 等 OpenSSH 选项
- Unix socket 转发
- `-W` stdio 转发
- `-D` 的 UDP ASSOCIATE、用户名密码认证
- 自动读取并启动 SSH config 中的 `LocalForward` / `RemoteForward`
- 断线自动重连
- 转发规则的持久化和开机恢复

`ProxyJump` 仍可使用，但方式与直接写 `ssh -J` 不同：whop 读取 alias 对应的 SSH config 中的 `ProxyJump`。`ProxyCommand` / `Match exec` 仍是 V1 非目标。

## 8. sshd 侧要求

端口转发不是 sshd 执行 shell 命令，而是 SSH 协议中的 channel/global request。sshd 会根据配置决定是否允许。

常见服务端配置：

```sshconfig
AllowTcpForwarding yes
PermitOpen any
PermitListen any
GatewayPorts clientspecified
```

生产环境不应直接照抄宽松配置。更安全的方式是限制目标：

```sshconfig
AllowTcpForwarding yes
PermitOpen db.internal:5432
PermitListen 127.0.0.1:9000
GatewayPorts no
```

说明：

- `AllowTcpForwarding` 控制是否允许 TCP forwarding。
- `PermitOpen` 限制 `-L` / `-D` 可连接的目标。
- `PermitListen` 限制 `-R` 可绑定的地址和端口。
- `GatewayPorts` 影响远端监听是否可以绑定到非 loopback 地址。
- 即使 sshd 允许，操作系统防火墙仍可能拦截远端监听端口。

## 9. 常见问题

### `address already in use`

监听端口已被占用。换一个本地或远端端口。

### `administratively prohibited`

sshd 拒绝了转发。检查 `AllowTcpForwarding`、`PermitOpen`、`PermitListen` 和 `GatewayPorts`。

### `connection refused`

监听已经建立，但目标地址或端口没有服务，或者从目标发起侧不可达。

### 远端能监听但不能从其他机器访问

通常是因为绑定了 `127.0.0.1`，或 `GatewayPorts` 不允许非 loopback 绑定，或远端防火墙未放行。

### `curl` 配置了 SOCKS5 仍然失败

确认使用的是 `--socks5-hostname` 或正确配置了 SOCKS5，而不是 HTTP 代理；同时确认 sshd 允许访问目标。

### 只有 `whop forward` 进程存活时转发才有效

这是预期行为。当前没有后台守护模式，进程退出后监听端口会关闭。

## 10. 快速对照表

| 需求 | OpenSSH | whop |
|---|---|---|
| 本地访问远端数据库 | `ssh -N -L 15432:db:5432 host` | `whop forward -L 15432:db:5432 alias` |
| 远端访问本机 Web | `ssh -N -R 9000:localhost:3000 host` | `whop forward -R 9000:localhost:3000 alias` |
| 本机 SOCKS5 代理 | `ssh -N -D 1080 host` | `whop forward -D 1080 alias` |
| 同时多条规则 | 多个 `-L/-R/-D` | 多个 `-L/-R/-D` |
| 使用 SSH config | `ssh host` | `whop forward ... alias` |
| 让命令留在前台 | 默认前台 | 默认前台 |
| 不打开 shell | `-N` | 内置行为 |
