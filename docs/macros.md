# Macro 语法与示例

本文是 `whop macro` 的完整参考。它解释 Macro 文件到底是什么、每一步如何执行，以及常见的登录、菜单和巡检流程怎样写。

如果你只记住一件事：

> **Macro 文件是一个完整的 JSON 对象，不是 JSONL，也不是“每行一条命令”。**  
> 文件中的换行只用于排版；真正有顺序的是 `steps` 数组里的对象。每个对象是一条操作。

例如下面两段内容对 whop 完全等价：

```json
{"version":1,"steps":[{"kind":"wait","pattern":"login: "},{"kind":"send","send":"admin","newline":true}]}
```

```json
{
  "version": 1,
  "steps": [
    {
      "kind": "wait",
      "pattern": "login: "
    },
    {
      "kind": "send",
      "send": "admin",
      "newline": true
    }
  ]
}
```

第二段只是更容易阅读。把每个步骤单独写在一行、但顶层仍是同一个 JSON 对象，也是合法的：

```json
{
  "version": 1,
  "steps": [
    {"kind": "wait", "pattern": "login: ", "timeout": "10s"},
    {"kind": "send", "send": "admin", "newline": true}
  ]
}
```

真正非法的写法是“每行一个独立 JSON 对象”的 JSONL：

```text
{"kind":"wait","pattern":"login: "}
{"kind":"send","send":"admin","newline":true}
```

`whop macro set` 只读取一个 JSON 值，后面再出现第二个 JSON 值会报 `unexpected trailing JSON value` 一类错误。

## 1. 整体模型

可以把 Macro 看成一个固定顺序的操作表：

```text
连接 SSH
  -> 执行第 1 步
  -> 执行第 2 步
  -> 执行第 3 步
  -> ...
  -> 关闭交互式会话
```

它没有条件分支、循环、变量替换或“匹配到 A 就跳转、否则执行 B”的能力。任一步失败，整个宏立即停止，后面的步骤不会执行。需要不同分支时，应保存成多个宏，按情况调用。

三个 `kind` 的含义分别是：

| `kind` | 做什么 | 不做什么 |
|---|---|---|
| `wait` | 从远端 **stdout** 中等待一个 Go 正则匹配 | 不发送任何内容，不会自动按回车 |
| `send` | 把指定字节原样写入远端终端 | 不等待命令完成，不自动判断 prompt |
| `sleep` | 暂停固定时间 | 不读取输出，不检测 prompt |

所谓 “expect” 通常就是下面两步的组合：

```json
[
  {"kind": "wait", "pattern": "Password: ", "timeout": "10s"},
  {"kind": "send", "secret_env": "WHOP_DEPLOY_PASSWORD", "newline": true}
]
```

先等输出里出现 `Password: `，再发送密码。`wait` 负责“等”，`send` 负责“发”，不会在一条步骤里同时完成两件事。

## 2. 顶层结构

一个 Macro 文件只能包含一个 JSON 对象：

```json
{
  "version": 1,
  "name": "login",
  "steps": [
    {"kind": "send", "send": "uptime", "newline": true}
  ]
}
```

| 字段 | 类型 | 是否必填 | 说明 |
|---|---|---|---|
| `version` | 整数 | 可选 | 当前只支持 `1`。省略时按 `1` 处理；建议始终显式写 `1`。 |
| `name` | 字符串 | 可选 | 文件内的名字不会决定保存名。执行 `whop macro set <name> <file>` 时，命令行给出的 `<name>` 才是宏名，并会覆盖此字段。 |
| `steps` | 数组 | 必填 | 至少包含一个步骤，按数组顺序执行。 |

文件上限为 1 MiB。JSON 不支持注释，也不接受未知字段；例如在步骤里写 `"password": "..."` 不会被忽略，而会直接报错。

保存在 SQLite 后，`whop macro show <name>` 会输出规范化后的完整 JSON，其中 `name` 是实际保存名。

## 3. 通用步骤字段

每个步骤都是 `steps` 数组中的一个对象：

```json
{
  "name": "wait-password",
  "kind": "wait",
  "pattern": "Password: ",
  "timeout": "10s",
  "retries": 1
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | 字符串 | 可选。用于错误和审计日志，例如 `macro "login" step 2 (wait-password)`。省略时自动命名为 `<macro>/step-N`。最多 64 个字符，不能包含控制字符。 |
| `kind` | 字符串 | 必填，只能是 `wait`、`send`、`sleep`。大小写和首尾空白会被忽略，实际写法建议统一用小写。 |

不同 `kind` 只接受自己相关的字段。把 `pattern` 写进 `send`、把 `send` 写进 `wait`、把 `sleep` 写进 `wait` 等，都会在保存时被拒绝，避免拼写错误被悄悄忽略。

## 4. `wait`：等待远端输出

### 4.1 字段

```json
{"kind": "wait", "pattern": "login: ", "timeout": "10s", "retries": 1}
```

| 字段 | 类型 | 是否必填 | 说明 |
|---|---|---|---|
| `pattern` | 字符串 | 必填 | Go 正则表达式，不能匹配空字符串。 |
| `timeout` | 字符串 | 可选 | 单次等待的超时时间，Go duration 格式，例如 `500ms`、`10s`、`2m`。省略时该步骤没有单独超时，但仍受整体 `--timeout` 限制。 |
| `retries` | 整数 | 可选 | 首次等待超时后额外重试的次数，默认 `0`。`retries: 1` 表示总共最多等待两轮。 |

`wait` 不接受 `send`、`secret_env`、`newline`、`sleep`。

### 4.2 它到底在等什么

`wait` 不是按行读取，也不是把每一行拿去做 shell 判断。它看的是 SSH 会话的**连续 stdout 字节流**，并在内部缓冲区里搜索正则。

执行规则如下：

1. 从“上一次匹配结束的位置”开始扫描已经收到的 stdout；
2. 找到第一个匹配就继续执行下一步；
3. 匹配前的已消费内容会被丢弃；
4. 匹配结束后、已经提前读到的内容会保留给后续 `wait`；
5. 未匹配时继续读取，直到本步骤超时或整体超时；
6. 远端 stderr 会单独显示，但**不参与** `wait` 匹配。

因此 `wait` 适合匹配稳定的文本片段，例如：

- `login: `
- `Password: `
- `Continue? [y/N]`
- `load average`
- `shell> `

不要把 `wait` 想成“读取完整一行”。即使远端一次输出 `login: password: shell>`，第一个 `wait` 匹配 `login: ` 后，后面的内容仍可能留给下一个 `wait`。

### 4.3 正则和 JSON 转义

`pattern` 使用 Go 的 `regexp`（RE2 语法）。它支持字符类、分组、`(?i)` 等常见语法，但不支持后向引用、lookahead、lookbehind 等 Perl 扩展。

JSON 和正则都会使用反斜杠，因此经常需要写两层转义：

| 想匹配/使用的正则 | JSON 中的 `pattern` |
|---|---|
| 字面量 `$` | `"\\$"` |
| 正则 `\s+` | `"\\s+"` |
| 字面量 `[y/N]` | `"\\[y/N\\]"` |
| 字面量问号 `?` | `"\\?"` |
| 任意空白后跟 `login:` | `"\\s*login:"` |

例如 `"pattern": "\\$ $"` 解码后的正则是 `\$ $`：匹配字面量 `$`、一个空格，并要求它是当前缓冲区末尾。

`wait` 会拒绝“能匹配空字符串”的 pattern，例如 `.*`、`a*`、`^`。这类 pattern 会在还没有任何输出时立即匹配，无法起到等待作用。

### 4.4 超时和重试

- `timeout` 只限制当前这一轮 `wait`。
- `retries` 是额外轮数，不是重发次数；重试时仍然只是继续等输出。
- CLI 的 `whop macro run --timeout` 是整次运行的硬上限，覆盖连接、认证、shell、所有 `wait`/`send`/`sleep`。
- 单步 `timeout` 和整体 `--timeout` 同时生效，谁先到就以谁为准。
- CLI 整体 `--timeout` 默认是 `30s`，需要更长流程时显式调大。
- 连接被远端关闭时，`wait` 会报 stream closed，不会继续重试。

等待期间，未匹配的 stdout 缓冲区最多保留 1 MiB；超过后会报 buffer limit 错误。这通常说明 pattern 一直没匹配，或设备输出了大量无关内容。

### 4.5 prompt 建议

优先等待短而稳定的文本，不要把整个彩色 prompt 当成 pattern：

```json
{"kind": "wait", "pattern": "login: ", "timeout": "10s"}
```

如果 prompt 包含 ANSI 颜色控制符、动态路径或时间，匹配整段很容易失败。可以退一步匹配其中不变的部分，例如只等 `Password:`、`#`、`>` 或某条命令输出的固定文字。

## 5. `send`：向远端发送内容

### 5.1 字段

```json
{"kind": "send", "send": "uptime", "newline": true}
```

```json
{"kind": "send", "secret_env": "WHOP_DEPLOY_PASSWORD", "newline": true}
```

| 字段 | 类型 | 是否必填 | 说明 |
|---|---|---|---|
| `send` | 字符串 | 与 `secret_env` 二选一 | 要发送的普通文本。内容会直接写入远端终端，不做本地 shell 解释。 |
| `secret_env` | 字符串 | 与 `send` 二选一 | 环境变量名。运行时读取变量值并发送，文件里只保存变量名。 |
| `newline` | 布尔值 | 可选 | 默认 `false`。为 `true` 时，如果内容末尾没有换行，会自动追加一个 LF（`\n`）。 |

`send` 不接受 `pattern`、`timeout`、`retries`、`sleep`。`send` 和 `secret_env` 不能同时出现；两者都为空也会报错。

### 5.2 回车和换行

`send` 发送的是字面字节。最常见的错误是只写命令、不发送回车：

```json
{"kind": "send", "send": "uptime"}
```

远端 shell 可能只是把 `uptime` 放在输入行里，并不会执行。推荐写：

```json
{"kind": "send", "send": "uptime", "newline": true}
```

或者显式把换行放进 JSON 字符串：

```json
{"kind": "send", "send": "uptime\n"}
```

两种写法都行。`newline: true` 不会重复追加：如果 `send` 已经以 `\n` 结尾，就保持原样。

需要发送其他控制字符时，也使用 JSON 转义，例如：

- `\n`：LF；
- `\r`：CR；
- `\t`：Tab；
- `\u001b`：ESC；
- `\u0003`：Ctrl-C。

`newline: true` 只追加 `\n`，不会自动转成 `\r\n`。如果某台老设备确实需要字面 CR，需要自己在 `send` 字符串中写入 `\r`。

### 5.3 `secret_env` 的规则

```json
{"kind": "send", "secret_env": "WHOP_DEPLOY_PASSWORD", "newline": true}
```

- 变量名必须符合 `[A-Za-z_][A-Za-z0-9_]*`。
- `whop macro set` 只检查变量名格式，不要求在保存时变量已经存在。
- 真正运行 `whop macro run` 前，whop 从自己的进程环境读取变量；变量不存在或值为空都会失败。
- 值只存在于运行时内存中，不写入 Macro JSON、SQLite、审计日志或命令行参数。
- 同一次宏运行中，一个 `secret` 发送步骤最多发送一次，不会自动重发。
- `secret_env` 只保护“引用的值”，不保护普通 `send`。普通 `send` 的命令文本仍会明文保存在 SQLite 中。

因此不要把密码直接写进 `send`，也不要把含秘密值的脚本提交到仓库。

## 6. `sleep`：固定等待

```json
{"kind": "sleep", "sleep": "500ms"}
```

| 字段 | 类型 | 是否必填 | 说明 |
|---|---|---|---|
| `sleep` | 字符串 | 必填 | Go duration 格式，且必须大于 0，例如 `100ms`、`2s`、`1m`。 |

`sleep` 不接受发送或等待字段。它只暂停固定时间，不读取输出，也不知道远端是否准备好。

使用顺序建议：

1. 能等 prompt 或输出时，优先用 `wait`；
2. 设备确实需要固定启动/刷新时间，且没有稳定输出可等时，才用 `sleep`；
3. 不要用越来越长的 `sleep` 掩盖 pattern 写错的问题。

## 7. 完整示例

### 7.1 登录后执行巡检

保存为 `health.json`：

```json
{
  "version": 1,
  "steps": [
    {
      "name": "wait-login",
      "kind": "wait",
      "pattern": "login: ",
      "timeout": "10s"
    },
    {
      "name": "send-user",
      "kind": "send",
      "send": "admin",
      "newline": true
    },
    {
      "name": "wait-password",
      "kind": "wait",
      "pattern": "Password: ",
      "timeout": "10s"
    },
    {
      "name": "send-password",
      "kind": "send",
      "secret_env": "WHOP_DEPLOY_PASSWORD",
      "newline": true
    },
    {
      "name": "wait-shell",
      "kind": "wait",
      "pattern": "[#$] $",
      "timeout": "10s"
    },
    {
      "name": "uptime",
      "kind": "send",
      "send": "uptime",
      "newline": true
    },
    {
      "name": "wait-uptime",
      "kind": "wait",
      "pattern": "load average",
      "timeout": "10s"
    },
    {
      "name": "disk",
      "kind": "send",
      "send": "df -h",
      "newline": true
    },
    {
      "name": "wait-disk",
      "kind": "wait",
      "pattern": "Filesystem|文件系统",
      "timeout": "10s"
    }
  ]
}
```

逐步解释：

1. 等待登录提示 `login: `；
2. 发送用户名并回车；
3. 等待密码提示；
4. 从环境变量读取密码并发送；
5. 等待 shell prompt；
6. 发送 `uptime` 命令；
7. 等待输出中出现 `load average`；
8. 发送 `df -h`；
9. 等待磁盘表格的表头。

保存和运行：

```bash
whop macro set health ./health.json
whop macro show health
whop macro run --timeout 60s health prod-db
```

如果密码变量没有设置，运行会在发送任何密码前失败：

```text
step 4: environment variable WHOP_DEPLOY_PASSWORD is not set
```

安全地准备环境变量的一种 POSIX shell 写法：

```bash
read -r -s WHOP_DEPLOY_PASSWORD
export WHOP_DEPLOY_PASSWORD
whop macro run --timeout 60s health prod-db
unset WHOP_DEPLOY_PASSWORD
```

这样可以避免把密码直接写进 shell 历史。

### 7.2 确认交互式菜单

```json
{
  "version": 1,
  "steps": [
    {
      "kind": "wait",
      "pattern": "Continue\\? \\[y/N\\]",
      "timeout": "20s"
    },
    {
      "kind": "send",
      "send": "y",
      "newline": true
    },
    {
      "kind": "wait",
      "pattern": "saved successfully",
      "timeout": "20s"
    }
  ]
}
```

这里 `pattern` 解码后的正则是 `Continue\? \[y/N\]`：

- `\\?` 在 JSON 中产生正则 `\?`，匹配字面量问号；
- `\\[` 和 `\\]` 产生 `\[`、`\]`，匹配字面量方括号；
- 不是把 `[y/N]` 当成字符类。

### 7.3 设备较慢时使用固定等待

```json
{
  "version": 1,
  "steps": [
    {"kind": "wait", "pattern": "Press any key", "timeout": "30s"},
    {"kind": "send", "send": " "},
    {"kind": "sleep", "sleep": "500ms"},
    {"kind": "send", "send": "2", "newline": true},
    {"kind": "wait", "pattern": "ready", "timeout": "30s"}
  ]
}
```

这里的 `sleep` 是明确的“给设备半秒刷新时间”。如果有稳定的 `ready` prompt 可以等待，就不要用 `sleep` 猜时间。

### 7.4 最短的单步宏

```json
{"version": 1, "steps": [{"kind": "send", "send": "uptime", "newline": true}]}
```

这是一个合法宏，但它发送后立刻结束，不保证远端命令已经完成，也不保证输出被完整读取。需要等待命令结果时，应再加一个 `wait`：

```json
{
  "version": 1,
  "steps": [
    {"kind": "send", "send": "uptime", "newline": true},
    {"kind": "wait", "pattern": "load average", "timeout": "10s"}
  ]
}
```

## 8. 保存、查看和运行

从文件保存：

```bash
whop macro set login ./login.json
```

从标准输入保存：

```bash
whop macro set login - <<'JSON'
{
  "version": 1,
  "steps": [
    {"kind": "send", "send": "uptime", "newline": true}
  ]
}
JSON
```

查看、列出和删除：

```bash
whop macro list
whop macro list --json
whop macro show login
whop macro delete login
```

运行：

```bash
whop macro run --timeout 30s login prod-db
whop macro run --json login prod-db
```

`--json` 在失败时输出结构化诊断。常见类别包括：

| category | 含义 |
|---|---|
| `automation_timeout` | 某个 `wait` 或整体超时 |
| `automation_stream_closed` | 等待期间远端关闭了流 |
| `automation_buffer_limit` | 未匹配输出超过 1 MiB |
| `canceled` | 用户取消运行 |

## 9. 排障顺序

宏失败时，按这个顺序检查：

1. 用 `whop macro show <name>` 确认保存的 pattern 和实际内容一致；
2. 先在 `whop connect <alias>` 里手工走一遍流程，记录真实输出；
3. 确认 `wait` 等待的是 stdout，而不是 stderr；
4. 检查 JSON 是否多转义或少了转义；
5. 检查 `send` 是否真的带了换行；
6. 把过长的 prompt pattern 缩短为稳定的文本片段；
7. 适当增大单步 `timeout` 和整体 `--timeout`；
8. 只有在没有稳定输出可等待时，才增加 `sleep`；
9. 用 `whop macro run --json <name> <alias>` 查看失败阶段和类别；
10. 用 `whop audit` 查看是哪一步失败，但不要期待审计日志包含命令正文或终端输出。

一个常见但容易忽略的问题是：设备把提示信息写到 stderr，而 `wait` 只扫描 stdout。此时手工连接能看到提示，但宏始终超时。需要根据设备行为调整远端命令或改用能产生 stdout 输出的交互方式。
