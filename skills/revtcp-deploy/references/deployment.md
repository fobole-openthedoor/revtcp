# 当前部署约定

本技能用于已建立的 revtcp 项目，不假定某个公网 IP 永远不变。`~/.config/revtcp/host.json` 可记录本机 checkout、可执行文件、私有状态目录和实例路径；每次修改前以实际 systemd/监听状态为准。

## 发现与隔离

1. 查看 `systemctl list-unit-files 'revtcp*'`、相应 `systemctl show` 和 `ss -lntp`。读取所需实例的 env/unit 文件，不输出私钥或上传令牌。
2. 确认公网 IP：AWS 实例可用 IMDSv2 只取 `public-ipv4`，令牌仅放内存；配合用户域名或外网地址信息确认。不要为此读取 IAM 凭据。
3. 根据用户映射选两个空闲端口。业务端口由用户指定，隧道端口可选相邻空闲端口，例如既有 7443/7444 后选 7445；不要无脑复用这些示例。
4. 新实例常用名 `revtcp-server-<业务端口>.service`。初始旧实例可能叫 `revtcp-server.service`，不能误认为所有实例均符合新命名。

## 新建 systemd 实例

优先使用已验证的 `/usr/local/lib/revtcp/revtcp`；确认版本和与客户端的兼容性。没有安装时按仓库文档构建或验证官方 Release 附件，再安装到稳定路径。

每个新映射使用如下独立路径（已存在时先判断是否为重试/维护，不覆盖）：

- `/root/.local/share/revtcp-deployment/<业务端口>/pki/`：签发用 CA 及全套证书，父目录 `0700`。
- `/etc/revtcp/<业务端口>/certs/`：运行所需 `server.crt`、`server.key`、`ca.crt`，root:revtcp，目录 `0750`、文件 `0640`。
- `/etc/revtcp/<业务端口>/server.env`：隧道与业务监听地址。
- `/etc/systemd/system/revtcp-server-<业务端口>.service`：单独 unit。

签发：`revtcp init --hosts 实际域名或IP --out 新的pki目录`。默认有效期 365 天，不重用其他映射 CA。服务用户 `revtcp` 无登录 shell；只有不存在时才创建。需要不同权限模型时遵循现有部署而不是覆盖账号。

env 文件示例：

```ini
REVTCP_TUNNEL_LISTEN=0.0.0.0:7445
REVTCP_PUBLIC_LISTEN=0.0.0.0:18802
```

unit 的关键内容，路径与端口替换为该实例：

```ini
[Unit]
Description=revtcp TCP reverse tunnel server (18802)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=revtcp
Group=revtcp
EnvironmentFile=/etc/revtcp/18802/server.env
ExecStart=/usr/local/lib/revtcp/revtcp server --listen ${REVTCP_TUNNEL_LISTEN} --expose ${REVTCP_PUBLIC_LISTEN} --cert /etc/revtcp/18802/certs/server.crt --key /etc/revtcp/18802/certs/server.key --ca /etc/revtcp/18802/certs/ca.crt
Restart=always
RestartSec=3
TimeoutStopSec=15
LimitNOFILE=16384
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

运行 `systemd-analyze verify`，再 `daemon-reload` 和只针对新 unit 的 `enable --now`。确认实际监听已出现，而不只看 Type=simple 服务刚启动时的 `active`。

需要防火墙变更时只处理用户映射所需端口。已有授权覆盖该部署的正常必要操作，但没有权限时不替用户更改计费或寻找不相关凭据。

## 验证范围

- 用临时本地 echo TCP 服务作为测试目标；临时客户端显式 `--target 临时地址` 覆盖配置里的生产目标。
- 通过实际隧道入口和业务端口验证二进制流、半关闭及适量并发，结束临时客户端。
- 新实例应拒绝另一映射的客户端证书。可只测试新实例，避免给已运行实例增加无关活动。
- 验证现有实例 PID/状态保持不变，新实例 `active/enabled` 且监听正确。维护已有实例时必须按用户允许的中断范围操作。
- Mac 自启脚本可做语法/plist 检查；没有 Mac 环境时不能宣称已完成真实开机或睡眠唤醒验证。

## 只重打包

沿用该实例已生效的证书、隧道和业务端口，只调用打包脚本。不要重新签发 CA、改监听地址、重启客户端或服务端。给更新包创建独立上传回执并保留旧链接；除非用户要求，不删除先前上传。
