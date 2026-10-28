---
name: revtcp-deploy
description: 为 revtcp 项目新增或维护独立 TCP 反向映射，部署服务端，生成带项目和下载链接的客户端配置包，并在用户要求时上传直链。适用于“再做一个端口转发”“给客户端配置包”等既有 revtcp 场景。
---

# revtcp 部署与配置交付

项目：https://github.com/fobole-openthedoor/revtcp

用户要的是实际可运行的端口映射和一个可直接转交的配置包。默认延续已建立的部署方式；不要把本技能当成修改其他服务或自动公开上传凭据的授权。

## 确定当前任务

- 区分新增映射、修改现有实例、只重打包、只上传。重打包/上传不需要重启服务或重新签发证书。
- 从上下文确定“客户端目标 → 服务端业务端口”；缺少目标地址/端口时，先问这项，同时做不依赖答案的检查。
- 先读本机 `~/.config/revtcp/host.json`（若存在），再检查实际服务、监听端口、配置和公网地址。该文件是路径提示，不是实时状态。
- 本机默认源码位置为 `/root/hacker/revtcp`，但以实际 checkout 和用户指定位置为准。无需另装其他技能即可运行随附脚本。

## 部署或修改服务

部署时读 [references/deployment.md](references/deployment.md)。已有明确部署指令就直接完成相应操作；技能不额外引入确认流程。没有部署授权的打包任务不要顺带部署。

非显然的重要约定：

- 每个独立映射使用独立隧道监听端口、业务端口、CA/证书及 systemd 实例。同一个服务端进程的所有认证客户端会竞争业务连接，并不按客户端名字路由。
- 新增时不抢占已用端口，也不重启或覆盖现有实例。现有证书适配现有映射，重打包时沿用。
- 客户端 `target` 是客户端所在机器可访问的地址；部署验证使用临时 echo 目标，通过 CLI 覆盖 target，不要在服务端误启动生产客户端配置而暴露服务端的同名本地端口。
- 功能验证后结束临时客户端；仅在新实例尚无真实客户端时清理其空闲测试连接。不能为了测试去重启用户正在使用的旧实例。
- 说明实际验证边界：本机通过公网 IP 回连成功，不等于已从用户 Mac 或其他网络验证。没有云防火墙权限时如实报告，不把 AWS 实例身份令牌或云凭据交付给客户端。

## 打包客户端：链接必须在包内

用 `scripts/package_client.py`，无需改写一遍打包逻辑。先查询实际已发布版本及附件，确认下载地址符合当前项目命名；默认采用已发布稳定版，不猜“最新版”。

```bash
gh release view --repo fobole-openthedoor/revtcp --json tagName,url,assets
python3 scripts/package_client.py \
  --project-root /path/to/revtcp \
  --cert-dir /private/path/to/instance/pki \
  --host SERVER_IP_OR_DNS --tunnel-port TUNNEL_PORT \
  --public-port PUBLIC_PORT --target CLIENT_HOST:PORT \
  --release-tag PUBLISHED_TAG --output-dir /private/new-output-directory
```

这里的 `scripts/` 相对于本技能目录。脚本只生成文件，不连接目标、不部署服务、不上传。

生成的 ZIP 包含：

- `client.json`、客户端证书/私钥和 CA 证书；不含 CA 私钥、服务端私钥、上传令牌。
- `README.txt` 和 `LINKS.txt`：**项目主页、发布页、配套版本、macOS 两种架构及其他平台下载链接、校验文件和部署文档链接**。不能只把这些链接放在聊天回复里。
- `bundle-info.json`：机器可读的项目、版本、下载和映射信息。
- 按业务端口隔离的 macOS `service.sh` 与 plist，可和原客户端并存。包内说明使用本包脚本及已下载的 `revtcp` 程序安装。

用 Go 程序的 `client --config ... --check-config` 检查配置；核对 ZIP 成员、端口、label、项目链接及私钥权限。现有版本配置包被修订时用新的输出目录与上传回执，不覆盖旧回执后冒充同一文件。

配置包写到 Git 忽略的 `dist/private/` 或仓库外的私有目录，权限 `0700/0600`。发布源码或技能时不暂存这些生成物。

## 用户要求上传直链时

读 [references/upload.md](references/upload.md)，使用 `scripts/upload_bundle.py`。只有用户本次或既有上下文已授权上传到该站点时才上传；普通配置生成不自动上传。

```bash
python3 scripts/upload_bundle.py /private/bundle.zip \
  --state /private/instance/upload-receipt.json
```

脚本从站点绑定的私有凭据文件或配套环境变量读取令牌，不接受把令牌硬编码进技能。默认保留 30 天；用户要求其他有限期限时用 `--retention-days`。若要求永久或接口改变，先按当前 API 约定适配，不将默认期限说成永久。

服务返回 `ready` 且直链跳转验证成功后才交付链接。失败按回执恢复，最多自动尝试规定次数，不用新 ID 掩盖同一上传冲突或绕过过期状态。

## 交付

简要给出实际映射、隧道地址、配置包直链、到期时间（到期会删除文件）及必要的验证限制。需要时说明包内已有项目和程序下载链接，接收方只需收到这一个包。
