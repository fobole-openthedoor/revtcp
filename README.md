# revtcp

独立的 Go TCP 反向隧道，只使用标准库，不依赖 frp，也不兼容 frp 的控制协议。

## 下载发布版

从仓库的 [Releases](https://github.com/fobole-openthedoor/revtcp/releases) 页面下载对应平台的编译成品。macOS 包内包含开机自启安装脚本、配置示例和部署文档；Linux 提供 AMD64 / ARM64 包，Windows 提供 AMD64 ZIP 包。

每个版本附带 `SHA256SUMS`，用于核对下载文件。Linux/macOS 在下载目录中运行 `shasum -a 256 -c SHA256SUMS`；只下载部分平台时，仅校验自己下载的对应条目。程序版本及构建信息可以通过 `revtcp version` 查看。

```text
访问者 ──TCP──> 公网服务端 :6000
                       │
                 已建立的 TLS 反向连接
                       │
                  内网客户端 ──TCP──> 127.0.0.1:8080

反向连接由内网客户端主动发起，连接公网服务端 :7443。
```

这里的 TCP raw 指透明转发任意 TCP 字节流：可以承载 SSH、数据库、自定义二进制协议等。业务入口和本地目标使用原始 TCP，中间隧道使用 TLS 1.3 + 双向证书认证。没有 HTTP、WebSocket、QUIC 或 UDP 依赖，也不是 IP raw socket。

## 延迟与协议特征

- 客户端预先建立并认证连接，默认保持 16 条空闲隧道。收到业务连接后立即补充空闲连接。
- 服务端发送 `OPEN` 后直接转发数据，不等待应用层确认。在有可用隧道时，业务建流无需额外的隧道握手往返。
- 每条业务连接独占一条隧道，不使用多路复用；一个连接的 TCP 丢包不会在同一条隧道中阻塞其他业务连接。业务连接结束后，隧道随之关闭。
- 首次 TCP/TLS 建连、本地目标 TCP 建连及数据传播仍有延迟。连接池耗尽、心跳正在等待回复或断线重连时，也可能需要等待。
- **这不是 TLS early data 意义上的 0-RTT**，不重放业务数据。TLS early data 的重放性质参见 [RFC 8446 §8](https://www.rfc-editor.org/rfc/rfc8446.html#section-8)。
- 线上没有 frp 的明文标识或 frp 消息格式。TLS 握手、包长、连接数量和时序仍可被观察；不承诺“无指纹”或绕过特定检测产品，也不将流量伪装成浏览器 HTTPS。

## 构建

Go 1.23 或更高版本；部署时使用仍受维护的 Go 工具链。

```bash
CGO_ENABLED=0 go build -trimpath -o bin/revtcp ./cmd/revtcp
```

一个二进制提供 `init`、`server`、`client` 三个子命令。

## 复用部署技能与客户端配置包

仓库内提供 [revtcp-deploy 技能](skills/revtcp-deploy/SKILL.md)，覆盖独立映射部署、客户端配置打包和按需上传直链。复制 `skills/revtcp-deploy` 到自己的 Codex skills 目录后，可以使用 `$revtcp-deploy` 调用。

技能的 `scripts/package_client.py` 会把项目主页、发布页、各平台下载链接和配置方法写入每份客户端 ZIP 的 `README.txt`、`LINKS.txt`，接收方只需要收到一个配置包。每个业务端口有独立的 macOS 自启服务名。真实配置包包含客户端私钥，应放在 `dist/private/` 或仓库外的私有目录；上传令牌和上传回执保存在仓库之外。

## macOS 后台客户端与开机自启

已提供 Apple Silicon / Intel 构建脚本、LaunchDaemon 安装及管理脚本、JSON 配置和轮转日志。
完整安装、修改配置、查看日志、更新证书、升级及卸载方法见 [macOS 部署文档](docs/macos.md)。

```bash
bash scripts/build-macos.sh
# 输出两个可复制到 Mac 的安装包：dist/revtcp-macos-{arm64,amd64}.tar.gz
```

客户端也可在各平台使用 JSON 配置：

```bash
./bin/revtcp client --config /path/to/client.json --check-config
./bin/revtcp client --config /path/to/client.json
```

字段见 [配置示例](deploy/macos/client.example.json)。命令行显式参数覆盖 JSON；配置中的相对证书和日志路径相对于 JSON 所在目录解析。

## 快速开始

下面把客户端的 `127.0.0.1:8080` 映射到公网服务端的 `:6000`。将 `tunnel.example.com` 换成真实的服务端域名或 IP。

### 1. 生成证书

```bash
./bin/revtcp init --hosts tunnel.example.com --out certs
# 也可以使用 IP，或逗号分隔的多个名称：
# ./bin/revtcp init --hosts 203.0.113.10,tunnel.example.com --out certs
```

生成的证书默认有效期为 365 天，可用 `--days` 设置。输出目录必须不存在，命令不会覆盖已有证书。

| 文件 | 放置位置 |
| --- | --- |
| `ca.crt` | 服务端和客户端 |
| `server.crt`、`server.key` | 服务端 |
| `client.crt`、`client.key` | 客户端 |
| `ca.key` | 离线保管，运行时不需要 |

私钥创建时的权限为 `0600`。两端默认从当前工作目录的 `certs/` 读取证书，也可以用 `--cert`、`--key`、`--ca` 指定路径。

### 2. 公网服务端

```bash
./bin/revtcp server \
  --listen :7443 \
  --expose :6000
```

客户端连接 `7443/tcp`；访问者连接 `6000/tcp`。这两个监听地址应使用不同端口，防火墙需允许对应连接。`--expose` 默认是 `127.0.0.1:6000`，示例中的 `:6000` 会监听所有网卡。

### 3. 内网客户端

```bash
./bin/revtcp client \
  --server tunnel.example.com:7443 \
  --target 127.0.0.1:8080 \
  --pool 16
```

访问 `tunnel.example.com:6000` 就会转发到客户端的 `127.0.0.1:8080`。客户端不需要开放入站端口。

如果通过 IP 连接，但证书只包含域名，使用：

```bash
./bin/revtcp client \
  --server 203.0.113.10:7443 \
  --server-name tunnel.example.com \
  --target 127.0.0.1:8080
```

### 单机验证

```bash
./bin/revtcp init --out certs
# 终端 1：本地测试服务
python3 -m http.server 8080 --bind 127.0.0.1
# 终端 2：服务端
./bin/revtcp server --listen 127.0.0.1:7443 --expose 127.0.0.1:6000
# 终端 3：客户端
./bin/revtcp client --server 127.0.0.1:7443 --target 127.0.0.1:8080
# 终端 4：验证映射
curl http://127.0.0.1:6000/
```

## 运行参数

使用 `./bin/revtcp server --help` 或 `./bin/revtcp client --help` 查看全部参数。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| 客户端 `--pool` | `16` | 希望维持的空闲连接数，包括正在连接和重试的槽位 |
| 客户端 `--max-conns` | `1024` | 空闲、连接中和工作中的隧道总上限，必须不小于 `pool` |
| 客户端 `--dial-timeout` | `10s` | 每次 TCP 拨号、TLS 握手、控制消息写入的超时 |
| 客户端 `--idle-timeout` | `90s` | 空闲隧道等待控制消息的超时，不应用于工作中的业务流 |
| 客户端 `--retry-min` / `--retry-max` | `250ms` / `15s` | 指数退避范围，每次实际等待含随机抖动 |
| 服务端 `--max-conns` | `4096` | 每个监听器的连接上限，分别限制隧道连接及含等待中的访问者连接 |
| 服务端 `--wait-timeout` | `10s` | 访问者等待可用反向连接的最长时间 |
| 服务端 `--handshake-timeout` | `10s` | 反向连接 TLS 握手超时 |
| 服务端 `--heartbeat` | `20s` | 空闲隧道心跳周期 |
| 服务端 `--heartbeat-timeout` | `5s` | 心跳回复和控制消息写入的超时 |
| 两端 `--debug` | `false` | 输出单连接、目标连接失败、转发错误等调试信息 |
| 客户端 `--config` | 空 | JSON 配置文件 |
| 客户端 `--check-config` | `false` | 检查配置、本地证书和日志路径后退出，不连接网络 |
| 两端 `--log-file` | 空 | 轮转日志文件，未设置时写 stderr |
| 两端 `--log-max-mb` / `--log-backups` | `10` / `3` | 日志轮转阈值（MiB）及保留份数 |

`idle-timeout` 应大于服务端 `heartbeat + heartbeat-timeout`，并留出网络抖动余量。增加 `pool` 有利于突发连接，但会占用更多套接字和预建 TLS 握手资源。`max-conns` 应为业务并发及空闲池留足容量；相关进程的文件描述符上限也需要足够大。

TCP 双向半关闭通过 TCP `CloseWrite` 和 TLS `close_notify` 传播，支持等待请求 EOF 才返回响应的协议。相关行为见 [Go TLS CloseWrite](https://pkg.go.dev/crypto/tls#Conn.CloseWrite)。工作中的隧道没有应用层心跳帧，也没有默认业务空闲超时；底层 TCP 启用 keepalive，其故障检测时长取决于操作系统。

收到 Ctrl+C 或 SIGTERM 时退出并关闭现有连接。客户端掉线后自动恢复空闲池；已经断开的业务连接由上层应用自行重连，程序不会自动重发请求。

## 范围

- 一个服务端进程对应一个业务监听端口，一个客户端进程对应一个固定目标。多端口映射可运行多组进程，并分配不同的隧道端口。
- 同一服务端上的认证客户端共同竞争业务连接，适合连接同一逻辑服务的副本。不提供客户端名称路由或租户隔离；不同映射应使用不同端口和独立 CA。
- 业务目标看到的是客户端进程的地址，不保留访问者源 IP，也不注入 PROXY protocol 头。
- 证书认证保护反向隧道；业务监听端口的访问权限仍由该服务自身的认证和网络访问规则控制。
- 断链与心跳检测之间存在短暂窗口，分配到失效隧道的业务连接可能失败；不做可能重复提交数据的透明重试。

## 验证

```bash
go test -race ./...
go vet ./...
```

集成测试使用真实 TCP 和 TLS，覆盖二进制并发传输、两个方向的半关闭、服务先发数据、连接池补充、服务端重启重连、空闲心跳、无证书/错误证书/错误主机名拒绝、连接限额、等待超时、目标不可用、活动连接取消，以及无需 `OPEN` 确认即可发送首批数据。

GitHub Actions 在 Linux 和 macOS 上运行竞态检测与静态检查。构建全部发布包及校验文件：

```bash
bash scripts/build-release.sh v0.1.0
# 输出 dist/v0.1.0/ 下的五个平台包和 SHA256SUMS
```

发布脚本需要 Go、Bash、tar、zip 和 shasum；macOS 专用构建脚本只需要 Go、Bash 和 tar。

交叉编译示例：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/revtcp-linux-amd64 ./cmd/revtcp
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/revtcp-linux-arm64 ./cmd/revtcp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o bin/revtcp-windows-amd64.exe ./cmd/revtcp
```
