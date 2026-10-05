#!/usr/bin/env python3
"""nodepanel 批量部署工具（走 SSH，把节点端装到远端服务器上）。

注意：本脚本不保存任何服务器信息。请通过参数或你自己的私有清单文件传入，
不要把真实 IP / 密码写进仓库（.gitignore 已忽略 servers*.json）。

用法:
  python3 deploy.py --host 1.2.3.4 --port 22 --password 'pw' [--agent-port 8899]
                    [--tunnel] [--tunnel-token eyJ...] [--name web-01]
  可选 --dry-run 只打印将要执行的动作。

依赖: paramiko
"""
import argparse
import base64
import os
import shlex
import socket
import sys
import time

try:
    import paramiko
except ImportError:
    sys.exit("需要 paramiko: pip install paramiko")

AGENT_BIN = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                         "dist", "nodemgr-agent")
REMOTE_TMP = "/tmp/nodemgr-agent.deploy"


def connect(host, port, user, password, timeout=20):
    cli = paramiko.SSHClient()
    cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    cli.connect(hostname=host, port=port, username=user, password=password,
                timeout=timeout, banner_timeout=timeout, auth_timeout=timeout,
                allow_agent=False, look_for_keys=False)
    return cli


def run(cli, cmd, timeout=180, quiet=False):
    stdin, stdout, stderr = cli.exec_command(cmd, timeout=timeout, get_pty=False)
    out = stdout.read().decode("utf-8", "replace")
    err = stderr.read().decode("utf-8", "replace")
    code = stdout.channel.recv_exit_status()
    if not quiet:
        for line in (out + err).splitlines():
            print("   |", line)
    return code, out, err


def detect_arch(cli):
    code, out, _ = run(cli, "uname -m", quiet=True)
    m = out.strip()
    return {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64",
            "arm64": "arm64", "armv7l": "armv7"}.get(m, "")


def upload(cli, local, remote):
    sftp = cli.open_sftp()
    try:
        sftp.put(local, remote)
        sftp.chmod(remote, 0o755)
    finally:
        sftp.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", required=True)
    ap.add_argument("--port", type=int, default=22)
    ap.add_argument("--user", default="root")
    ap.add_argument("--password", required=True)
    ap.add_argument("--agent-port", type=int, default=8899, help="节点端监听端口，0=仅内网穿透")
    ap.add_argument("--agent-password", default=None, help="节点管理/SSH 密码，默认与 SSH 密码相同")
    ap.add_argument("--tunnel", action="store_true")
    ap.add_argument("--tunnel-mode", default="quick", choices=["quick", "token"])
    ap.add_argument("--tunnel-token", default="")
    ap.add_argument("--name", default="")
    ap.add_argument("--bind", default="0.0.0.0", help="agent 监听地址，127.0.0.1 = 只允许 SSH 隧道访问")
    ap.add_argument("--bin", default=AGENT_BIN)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    agent_pw = args.agent_password or args.password

    print(f"==> 连接 {args.user}@{args.host}:{args.port}")
    cli = connect(args.host, args.port, args.user, args.password)

    code, out, _ = run(cli, "id -un; uname -m; . /etc/os-release 2>/dev/null; echo $PRETTY_NAME; "
                            "hostname; (command -v cloudflared || echo no-cloudflared); "
                            "(systemctl is-active nodemgr-agent 2>/dev/null || echo inactive)",
                       quiet=True)
    info = [l.strip() for l in out.strip().splitlines()]
    print("    用户:", info[0] if info else "?")
    print("    架构:", info[1] if len(info) > 1 else "?")
    print("    系统:", info[2] if len(info) > 2 else "?")
    hostname = info[3] if len(info) > 3 else ""
    print("    主机名:", hostname)
    print("    cloudflared:", info[4] if len(info) > 4 else "?")
    print("    agent 状态:", info[5] if len(info) > 5 else "?")

    arch = detect_arch(cli)
    if not args.bin or not os.path.exists(args.bin):
        sys.exit(f"找不到本地的 nodemgr-agent: {args.bin}（先执行 scripts/build.sh）")

    install_cmd = [
        REMOTE_TMP, "install",
        "-port", str(args.agent_port),
        "-password", agent_pw,
        "-ssh-password", agent_pw,
    ]
    # 绑定 127.0.0.1 时不占用公网端口，只能通过 SSH 隧道访问（更安全）。
    if args.bind != "0.0.0.0":
        install_cmd += ["-bind", args.bind]
    if args.tunnel:
        install_cmd += ["-tunnel", "-tunnel-mode", args.tunnel_mode]
        if args.tunnel_token:
            install_cmd += ["-tunnel-token", args.tunnel_token]

    print(f"==> 上传节点端二进制 ({arch})")
    if args.dry_run:
        print("    [dry-run] sftp put", args.bin, "->", REMOTE_TMP)
    else:
        upload(cli, args.bin, REMOTE_TMP)

    print("==> 执行安装:", " ".join(shlex.quote(c) for c in install_cmd))
    if args.dry_run:
        print("    [dry-run] 跳过")
    else:
        code, out, err = run(cli, " ".join(shlex.quote(c) for c in install_cmd), timeout=300)
        if code != 0:
            print(f"!! 安装失败 (exit {code})")
            cli.close()
            sys.exit(1)
        token = ""
        for line in out.splitlines():
            if "访问令牌" in line:
                token = line.split(":")[-1].strip()

    if not args.dry_run:
        print("==> 校验服务")
        time.sleep(2)
        run(cli, "systemctl is-active nodemgr-agent", quiet=False)
        run(cli, "ss -ltnp 2>/dev/null | grep -E ':8899|:10005' || netstat -ltnp 2>/dev/null | grep 8899 || true")
        if args.tunnel:
            time.sleep(6)
            run(cli, "journalctl -u nodemgr-agent -n 40 --no-pager | grep -E 'trycloudflare|公网地址|error' || true")

    print("\n================ 结果 ================")
    print("主机:", args.host, "(" + hostname + ")")
    print("节点端口:", args.agent_port)
    print("访问令牌:", token if not args.dry_run else "(dry-run)")
    print("管理密码:", agent_pw)
    cli.close()


if __name__ == "__main__":
    main()
