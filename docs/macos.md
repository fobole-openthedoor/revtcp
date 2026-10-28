# macOS 客户端：后台运行、开机自启与配置

项目主页：https://github.com/fobole-openthedoor/revtcp

程序下载：https://github.com/fobole-openthedoor/revtcp/releases/latest

客户端以 macOS **系统级 LaunchDaemon** 运行。启动系统后由 launchd 启动，无需保持终端打开，也不依赖桌面用户登录。进程退出后自动重新启动；网络不可用时，客户端自己持续退避重连。

服务名为 `local.revtcp.client`。安装需要管理员权限，但客户端进程使用指定的**普通本地用户**身份运行，默认是执行 `sudo` 的用户。这是可查看、可停用、可卸载的普通系统服务。LaunchDaemon 和登录后才启动的 LaunchAgent 的区别见 [Apple launchd 文档](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)。

## 1. 选择安装包

在 Mac 上检查架构：

```bash
uname -m
```

| 输出 | 安装包 |
| --- | --- |
| `arm64`，Apple Silicon | `revtcp-macos-arm64.tar.gz` |
| `x86_64`，Intel | `revtcp-macos-amd64.tar.gz` |

在使用 Rosetta 的终端中可能看到 `x86_64`；Apple Silicon 优先使用原生 arm64 包。

```bash
# Apple Silicon 示例；Intel 替换成 amd64
tar -xzf revtcp-macos-arm64.tar.gz
cd revtcp-macos-arm64
chmod +x revtcp service.sh
./revtcp --help
```

包内包含客户端程序、服务管理脚本、launchd 模板、配置示例及本文档。目标 Mac 不需要安装 Go、Python 或 Homebrew。安装包没有 Developer ID 公证；如果 macOS 阻止运行，可在自己信任的源码目录中本机编译使用，无需关闭系统安全功能。

从源码构建当前 Mac 的版本：

```bash
CGO_ENABLED=0 go build -trimpath -o bin/revtcp ./cmd/revtcp
```

在源码目录中构建两个架构的安装包：

```bash
bash scripts/build-macos.sh
# 输出 dist/revtcp-macos-arm64.tar.gz、dist/revtcp-macos-amd64.tar.gz
```

## 2. 准备服务端及证书

以下示例把 Mac 上的 `127.0.0.1:8080` 暴露为服务器的 `:6000`，隧道入口为 `:7443`。

如果已经部署了服务端，沿用它的 CA 和配套客户端证书，不要在 Mac 上另外生成一套无关 CA。

新部署时，在可信机器上生成证书，并在服务端启动：

```bash
./revtcp init --hosts tunnel.example.com --out certs
./revtcp server --listen :7443 --expose :6000
```

`tunnel.example.com` 替换为真实服务器域名；直接通过 IP 连接时，`--hosts` 应包含该 IP。服务端需要允许客户端连接 `7443/tcp`，以及允许业务访问者连接 `6000/tcp`。

通过已有的可信传输方式，把以下三个文件放到 Mac 安装包目录的 `certs/` 下：

```text
certs/ca.crt
certs/client.crt
certs/client.key
```

Mac 不需要服务端私钥和 `ca.key`。客户端本地目标服务必须能被所选运行用户通过 TCP 访问。

## 3. 编辑客户端配置

在解压后的包目录执行：

```bash
cp client.example.json client.json
nano client.json
```

通常先修改 `server` 和 `target`：

```json
{
  "server": "tunnel.example.com:7443",
  "target": "127.0.0.1:8080",
  "server_name": "",
  "cert": "certs/client.crt",
  "key": "certs/client.key",
  "ca": "certs/ca.crt",
  "pool": 16,
  "max_conns": 1024,
  "dial_timeout": "10s",
  "idle_timeout": "90s",
  "retry_min": "250ms",
  "retry_max": "15s",
  "debug": false,
  "log_max_mb": 10,
  "log_backups": 3
}
```

| 配置项 | 配置方法 |
| --- | --- |
| `server` | 服务端**隧道**地址，例如 `203.0.113.10:7443`；不是业务映射端口 |
| `target` | 从 Mac 访问的业务地址，例如 `127.0.0.1:8080`、`192.168.1.10:22`；IPv6 写成 `[::1]:8080` |
| `server_name` | 默认为空，校验 `server` 中的主机名；如果通过 IP 连接但证书只包含域名，填写证书域名 |
| `cert` / `key` / `ca` | 安装脚本使用上面的相对路径，分别复制客户端证书、私钥和 CA 到 `config/certs/` |
| `pool` | 希望维持的空闲反向连接数，默认 `16` |
| `max_conns` | 空闲、连接中及工作中的隧道总上限，默认 `1024`，必须不小于 `pool` |
| `dial_timeout` | 每次连接和 TLS 握手超时，例如 `10s` |
| `idle_timeout` | 空闲隧道的控制读超时，须大于服务端心跳周期加回复超时；默认服务端为 `20s + 5s` |
| `retry_min` / `retry_max` | 重连指数退避参数，实际等待包含随机抖动 |
| `debug` | 排查单连接问题时设为 `true`；正常长期运行建议 `false` |
| `log_max_mb` / `log_backups` | 单个应用日志的轮转阈值与历史份数，默认 `10 MiB`、`3` 份 |

JSON 使用上述下划线字段名，不支持注释和多余逗号；未知字段和错误类型会报错。未填写的字段采用程序默认值。地址要求非空主机名/IP 和 `1..65535` 的数字端口。

**路径规则：**配置文件中相对的证书和日志路径，相对于 JSON 所在目录解析，和启动时的工作目录无关。不展开 `~` 或 `$HOME`。显式命令行选项优先于文件；命令行相对路径相对于当前工作目录解析。

可先在前台验证：

```bash
./revtcp client --config ./client.json --check-config
./revtcp client --config ./client.json --debug
# 在另一台能访问服务器的机器上连接 tunnel.example.com:6000
# 确认后按 Ctrl+C 结束前台进程，再安装后台服务。
```

`--check-config` 检查参数、本地证书文件能否加载以及日志路径是否可打开，必要时创建日志文件；它不连接服务端，不验证远端可达性，也不代替实际 TLS 握手对证书信任链、名称及有效期的校验。

## 4. 安装并开启开机自启

在解压后的安装包目录执行：

```bash
sudo ./service.sh install \
  --binary ./revtcp \
  --config ./client.json \
  --cert-dir ./certs
```

指定其他普通本地用户运行时：

```bash
sudo ./service.sh install \
  --binary ./revtcp \
  --config ./client.json \
  --cert-dir ./certs \
  --user alice
```

从源码目录安装时，将脚本替换为 `./deploy/macos/service.sh`，程序替换为 `./bin/revtcp`，其他参数指向自己准备的文件。

脚本先将程序、配置及证书复制到临时目录，并以实际运行用户进行离线配置检查；通过后才安装并注册服务。安装会立即启动客户端，并设置之后开机自动启动。安装不会修改服务端配置，也不会修改 Mac 的睡眠策略。

| 安装内容 | 位置 |
| --- | --- |
| 程序 | `/Library/Application Support/revtcp/bin/revtcp` |
| JSON 配置 | `/Library/Application Support/revtcp/config/client.json` |
| 客户端证书和私钥 | `/Library/Application Support/revtcp/config/certs/` |
| 管理脚本 | `/Library/Application Support/revtcp/service.sh` |
| launchd 配置 | `/Library/LaunchDaemons/local.revtcp.client.plist` |
| 应用日志 | `/Library/Logs/revtcp/client.log` |
| 启动错误日志 | `/Library/Logs/revtcp/launchd.log` |

程序和 plist 由 root 管理；配置、私钥和日志目录仅运行用户及 root 可访问。安装完成后不依赖原来的解压目录，可以把它移走。

如果安装中途失败，保留已安装文件以便检查；确认原因后可运行下面的 `uninstall --purge` 清理，再安装。脚本不会覆盖现有安装或遗留数据。

## 5. 查看状态、日志及管理自启

安装之后统一使用已安装的脚本。在当前终端定义路径：

```bash
svc='/Library/Application Support/revtcp/service.sh'
sudo "$svc" status
sudo "$svc" logs
```

`status` 中 `state = running` 说明进程运行，不代表隧道认证和目标服务一定成功。`logs` 同时跟踪应用日志及启动错误；Ctrl+C 只结束日志查看，不会停止服务。端到端验证仍应访问服务器的业务映射端口。

```bash
# 检查当前配置及文件权限，以实际运行用户执行
sudo "$svc" check

# 重新读取配置并重启；当前业务连接会断开
sudo "$svc" restart

# 停止，并禁止下次开机启动
sudo "$svc" stop

# 恢复启动，并重新开启开机自启
sudo "$svc" start
```

应用日志按配置轮转，默认保留当前文件与 `.1`、`.2`、`.3`，约 `40 MiB` 加单条日志的余量。`.1` 是最近一次轮转的日志。多个进程不应同时写同一个轮转日志文件。

`launchd.log` 只接收启动前错误和进程异常输出，不参与应用日志轮转。如果它持续增长，应先修复启动错误；需要清空时执行 `sudo sh -c ': > /Library/Logs/revtcp/launchd.log'`。

## 6. 修改配置、更新证书及升级程序

修改配置并检查，然后重启：

```bash
sudo cp '/Library/Application Support/revtcp/config/client.json' \
  '/Library/Application Support/revtcp/config/client.json.bak'
sudo nano '/Library/Application Support/revtcp/config/client.json'
sudo "$svc" check
# check 成功后执行：
sudo "$svc" restart
```

`restart` 自身也会先检查配置，检查失败时保留现有进程。程序不自动热加载 JSON 或证书。

证书默认有效期是生成时设置的 `365` 天。更新时，使用与服务端信任配置配套的新客户端证书；在**运行用户自己的终端**中复制，保留目标文件的受限权限：

```bash
cp ./new-certs/client.crt '/Library/Application Support/revtcp/config/certs/client.crt'
cp ./new-certs/client.key '/Library/Application Support/revtcp/config/certs/client.key'
# 更换 CA 时，还需同步更新两端信任配置和配套证书，不能只换客户端 CA。
sudo "$svc" check
sudo "$svc" restart
```

只升级程序、不改变配置及证书：

```bash
sudo "$svc" upgrade --binary ./revtcp
```

升级会先用新程序校验现有配置，保存旧程序为 `bin/revtcp.previous`，再重启。需要回滚时：

```bash
sudo "$svc" upgrade \
  --binary '/Library/Application Support/revtcp/bin/revtcp.previous'
```

## 7. 停用和卸载

仅停用且保留完整安装，之后可随时 `start`：

```bash
sudo "$svc" stop
```

移除自启注册及 plist，保留程序、配置、证书和日志：

```bash
sudo "$svc" uninstall
```

彻底移除本工具的安装目录、证书、日志及自启项：

```bash
sudo "$svc" uninstall --purge
```

`--purge` 会删除安装目录内的私钥及备份，先把要保留的文件复制到其他位置。保留数据的卸载完成后，重新安装需要先备份这些数据，再执行 `--purge` 清理旧目录。

## 8. 长期运行的边界与故障排查

- **睡眠、合盖、关机：**正常睡眠期间不能保证转发。唤醒后客户端按心跳超时及重连退避恢复空闲池；已有业务连接可能中断。需要持续在线时，按场景调整 [macOS 供电与睡眠设置](https://support.apple.com/guide/mac-help/set-sleep-and-wake-settings-mchle41a6ccd/mac)；脚本不自动禁止睡眠。
- **FileVault：**磁盘尚未解锁、系统启动未完成时，LaunchDaemon 不能代替解锁步骤。开机自启不表示断电后一定能无人值守恢复，参见 [Apple FileVault 说明](https://support.apple.com/guide/security/volume-encryption-with-filevault-sec4c6dc1b6e/web)。
- **本地业务服务：**若目标应用只在桌面登录后启动，隧道进程可以先启动，但目标服务就绪前无法转发业务。
- **后台权限：**如果系统设置中禁用了相应后台项目，需由管理员恢复允许。[Apple 登录项和后台任务说明](https://support.apple.com/guide/mac-help/open-items-automatically-when-you-log-in-mh15189/mac)。
- **证书报错：**核对 `server_name`、CA、证书有效期和系统时间；不要通过跳过证书校验来解决。
- **`bootstrap` 失败：**先检查 plist、路径和用户权限，再查看启动日志。以下命令不启动业务连接：

```bash
sudo plutil -lint /Library/LaunchDaemons/local.revtcp.client.plist
sudo /usr/libexec/PlistBuddy -c 'Print :UserName' /Library/LaunchDaemons/local.revtcp.client.plist
sudo "$svc" check
sudo launchctl print system/local.revtcp.client
sudo tail -n 100 /Library/Logs/revtcp/launchd.log
```

同一台 Mac 的脚本安装目前只管理一个 `local.revtcp.client` 实例。多个映射需要独立 label、配置、日志和服务端隧道端口，避免覆盖同一个服务。

## 验证范围

自动化验证覆盖 Go 配置解析、命令行覆盖、证书相对路径、错误配置拒绝、日志轮转及重启后追加、并发写日志，以及原有 TCP/TLS 隧道集成测试。发布脚本生成 macOS ARM64 和 AMD64 Mach-O 二进制；脚本做 Bash 语法检查，plist 做结构检查。

当前开发环境为 Linux，不能实际运行 macOS 的 launchd。因此真实 Mac 上的启动、退出后拉起、重启自启和睡眠唤醒仍需实机验证。安装后可依次检查 `status`、服务端映射端口、重启 Mac 后的状态，确认端到端结果。
