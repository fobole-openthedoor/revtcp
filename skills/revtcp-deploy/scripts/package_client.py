#!/usr/bin/env python3
"""Build a private revtcp client bundle with project links and an isolated macOS service."""
import argparse
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import tempfile
import urllib.parse
import zipfile


PROJECT_URL = "https://github.com/fobole-openthedoor/revtcp"


def port(value):
    value = int(value)
    if not 1 <= value <= 65535:
        raise argparse.ArgumentTypeError("port must be 1..65535")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--cert-dir", type=Path, required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--tunnel-port", type=port, required=True)
    parser.add_argument("--public-port", type=port, required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--release-tag", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9._:%-]+", args.host):
        parser.error("host must be a DNS name or IP without a port or IPv6 brackets")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?", args.release_tag):
        parser.error("release-tag must name a published version such as v0.1.0")
    try:
        target = urllib.parse.urlsplit("//" + args.target)
        valid_target = target.hostname and target.port and not target.username and not target.password and not target.path and not target.query and not target.fragment
    except ValueError:
        valid_target = False
    if not valid_target:
        parser.error("target must be host:port (IPv6: [address]:port)")
    if args.tunnel_port == args.public_port:
        parser.error("tunnel and public ports must be different")

    os.umask(0o077)
    args.output_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    name = f"revtcp-client-{args.public_port}"
    archive = args.output_dir / (name + ".zip")
    destination = args.output_dir / name
    if archive.exists() or destination.exists():
        parser.error("output already exists; choose a new output directory to preserve prior bundles")
    template_dir = args.project_root / "deploy/macos"
    for file in [template_dir / "service.sh", template_dir / "local.revtcp.client.plist", template_dir / "client.example.json"]:
        if not file.is_file():
            parser.error(f"missing project template: {file}")
    for filename in ("ca.crt", "client.crt", "client.key"):
        if not (args.cert_dir / filename).is_file():
            parser.error(f"missing certificate: {filename}")

    host = f"[{args.host}]" if ":" in args.host else args.host
    server = f"{host}:{args.tunnel_port}"
    public = f"{host}:{args.public_port}"
    label = f"local.revtcp.client.{args.public_port}"
    app_dir = f"/Library/Application Support/revtcp-{args.public_port}"
    log_dir = f"/Library/Logs/revtcp-{args.public_port}"
    release_url = f"{PROJECT_URL}/releases/tag/{args.release_tag}"
    downloads = {
        platform: f"{PROJECT_URL}/releases/download/{args.release_tag}/revtcp-{args.release_tag}-{platform}.{extension}"
        for platform, extension in [
            ("macos-arm64", "tar.gz"), ("macos-amd64", "tar.gz"),
            ("linux-amd64", "tar.gz"), ("linux-arm64", "tar.gz"),
            ("windows-amd64", "zip"),
        ]
    }
    links = "\n".join([
        f"项目主页：{PROJECT_URL}",
        f"最新发布：{PROJECT_URL}/releases/latest",
        f"本包配套版本：{release_url}",
        f"macOS Apple Silicon：{downloads['macos-arm64']}",
        f"macOS Intel：{downloads['macos-amd64']}",
        f"Linux AMD64：{downloads['linux-amd64']}",
        f"Linux ARM64：{downloads['linux-arm64']}",
        f"Windows AMD64：{downloads['windows-amd64']}",
        f"校验文件：{PROJECT_URL}/releases/download/{args.release_tag}/SHA256SUMS",
        f"macOS 配置文档：{PROJECT_URL}/blob/{args.release_tag}/docs/macos.md",
    ]) + "\n"

    def specialize(text):
        replacements = {
            "/Library/Application Support/revtcp": app_dir,
            "/Library/Logs/revtcp": log_dir,
            "local.revtcp.client": label,
        }
        for old, new in replacements.items():
            if old not in text:
                raise ValueError(f"project template changed; expected marker missing: {old}")
            text = text.replace(old, new)
        return text

    with tempfile.TemporaryDirectory(prefix="bundle-", dir=args.output_dir) as temporary:
        stage = Path(temporary)
        package = stage / name
        (package / "certs").mkdir(parents=True, mode=0o700)
        config = json.loads((template_dir / "client.example.json").read_text())
        config.update(server=server, target=args.target, server_name="", cert="certs/client.crt", key="certs/client.key", ca="certs/ca.crt")
        (package / "client.json").write_text(json.dumps(config, indent=2) + "\n")
        # Copy only the client identity and trust root; never CA/server private keys.
        for filename in ("ca.crt", "client.crt", "client.key"):
            shutil.copyfile(args.cert_dir / filename, package / "certs" / filename)
            (package / "certs" / filename).chmod(0o600)
        (package / "service.sh").write_text(specialize((template_dir / "service.sh").read_text()))
        (package / "service.sh").chmod(0o755)
        plist = specialize((template_dir / "local.revtcp.client.plist").read_text())
        (package / f"{label}.plist").write_text(plist)
        parsed = plistlib.loads(plist.replace("__RUN_USER__", "exampleuser").encode())
        if parsed["Label"] != label or parsed["ProgramArguments"][3] != app_dir + "/config/client.json":
            raise ValueError("generated launchd identity/paths do not match this mapping")
        subprocess.run(["bash", "-n", str(package / "service.sh")], check=True)
        (package / "LINKS.txt").write_text(links, encoding="utf-8")
        (package / "README.txt").write_text(f"""revtcp {args.public_port} 客户端配置

{links}
客户端目标：{args.target}
业务访问地址：{public}
反向隧道入口：{server}

本配置包包含 client.json、配套客户端证书和私钥、独立 macOS 自启脚本。
按上方链接下载适合本机的程序。macOS 用 uname -m 查看架构：arm64 对应 Apple Silicon，x86_64 对应 Intel。
可把下载包内的 revtcp 程序复制到本配置包目录；使用本配置包附带的 service.sh。

进入本配置包目录，前台验证：
./revtcp client --config ./client.json --check-config
./revtcp client --config ./client.json

确认后按 Ctrl+C 结束前台进程，再安装后台运行及开机自启：
sudo ./service.sh install --binary ./revtcp --config ./client.json --cert-dir ./certs

本实例服务名：{label}
安装目录：{app_dir}
日志目录：{log_dir}
可与其他端口实例同机运行，不替换其配置。

管理命令：
sudo "{app_dir}/service.sh" status
sudo "{app_dir}/service.sh" logs
sudo "{app_dir}/service.sh" check
sudo "{app_dir}/service.sh" restart
sudo "{app_dir}/service.sh" stop
sudo "{app_dir}/service.sh" start
sudo "{app_dir}/service.sh" uninstall

stop 同时关闭自启，start 恢复；uninstall 保留文件，附加 --purge 才删除配置、证书和日志。
已有本实例安装时，替换 {app_dir}/config 下的 client.json 和 certs/ 文件，保留原运行用户及权限，随后 check、restart。
请在需要映射的客户端运行此配置。目标服务应已启动，睡眠期间无法保证持续转发。
""", encoding="utf-8")
        (package / "bundle-info.json").write_text(json.dumps({
            "project_url": PROJECT_URL, "release_url": release_url,
            "release_tag": args.release_tag, "downloads": downloads,
            "server": server, "target": args.target, "public_address": public,
            "macos_label": label,
        }, indent=2) + "\n")
        temporary_zip = stage / "bundle.zip"
        with zipfile.ZipFile(temporary_zip, "w", zipfile.ZIP_DEFLATED) as output:
            for file in sorted(package.rglob("*")):
                if file.is_file():
                    output.write(file, file.relative_to(stage))
        temporary_zip.chmod(0o600)
        package.rename(destination)
        temporary_zip.rename(archive)
    print(json.dumps({"archive": str(archive.resolve()), "directory": str(destination.resolve()), "server": server, "target": args.target, "public_address": public, "project_url": PROJECT_URL}, ensure_ascii=False))


if __name__ == "__main__":
    main()
