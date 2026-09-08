#!/usr/bin/env python3
"""Build four deterministic Platform connector ZIPs from a WorkBuddy catalog.

Only connector definitions and skill resources are read. User credentials and
installed WorkBuddy runtime state are never copied.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import zipfile

SPECS = {
    "tmeet": ("腾讯会议", "cli", "cli"),
    "tencent-docs": ("腾讯文档", "mcp", "oauth"),
    "tdx-connector": ("通达信", "mcp", "oauth"),
    "wecom": ("企业微信", "cli", "cli"),
}

LAUNCHER = r'''#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const {spawn} = require('node:child_process');
const pkgDir = path.resolve(__dirname, '..');
const manifest = JSON.parse(fs.readFileSync(path.join(pkgDir, 'connector.json'), 'utf8'));
const cli = JSON.parse(fs.readFileSync(path.join(pkgDir, 'cli.json'), 'utf8'));
const state = JSON.parse(fs.readFileSync(path.join(pkgDir, '.platform-runtime.json'), 'utf8')).stateDir;
const entry = path.join(state, 'npm', 'node_modules', cli.platform.npmPackage, cli.platform.entry);
if (!fs.existsSync(entry)) {
  console.error('Connector CLI is not prepared. Start login from Platform connector management.');
  process.exit(1);
}
const env = {...process.env, [cli.platform.configEnv]: path.join(state, 'config')};
const child = spawn(process.execPath, [entry, ...process.argv.slice(2)], {env, stdio: 'inherit'});
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
child.on('error', () => { console.error('Cannot start connector CLI'); process.exit(1); });
child.on('exit', code => process.exit(code === null ? 1 : code));
'''


def write_json(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")


def cli_package(source, dest, key):
    cli = json.loads((source / "cli.json").read_text())
    command = "tmeet" if key == "tmeet" else "wecom-cli"
    cli["platform"] = {
        "npmPackage": "@tencentcloud/tmeet" if key == "tmeet" else "@wecom/cli",
        "npmVersion": "1.0.16" if key == "tmeet" else "1.2.0",
        "entry": "scripts/tmeet.js" if key == "tmeet" else "bin/wecom.js",
        "command": command,
        "configEnv": "TMEET_CLI_CONFIG_DIR" if key == "tmeet" else "WECOM_CLI_CONFIG_DIR",
    }
    # Installation is performed by Platform's pinned, package-local npm runner.
    cli.pop("init", None)
    if key == "wecom":
        cli["platform"]["logoutMode"] = "delete-config"
        cli["unAuth"] = {"darwin": "", "linux": "", "win32": ""}
        cli["status"] = {os: f"{command}{'.cmd' if os == 'win32' else ''} auth show --status" for os in ("darwin", "linux", "win32")}
        cli["statusMatch"] = r"(?m)^authorized\s*$"
    else:
        cli["statusMatch"] = r"(?m)^Logged in\b"
    write_json(dest / "cli.json", cli)
    bindir = dest / "bin"
    bindir.mkdir()
    (bindir / "launcher.cjs").write_text(LAUNCHER)
    (bindir / command).write_text('#!/bin/sh\nexec node "$(dirname "$0")/launcher.cjs" "$@"\n')
    (bindir / command).chmod(0o755)
    (bindir / (command + ".cmd")).write_bytes(b'@echo off\r\nnode "%~dp0launcher.cjs" %*\r\n')


def adapt_skills(dest, key):
    if key == "tmeet":
        skill = dest / "skills/SKILL.md"
        text = skill.read_text()
        start = text.index("## 安装与初始化")
        end = text.index("## 命令总览与详情索引")
        text = text[:start] + """## Platform 安装与授权

本技能随 tmeet 连接器挂载，CLI 由 Platform 管理端准备并锁定版本。
首次使用请从 Platform 连接器管理发起登录，打开返回的 authorizationUrl 完成腾讯会议授权。
通过包内 bin/tmeet 调用，启动器会设置独立的 TMEET_CLI_CONFIG_DIR。
使用 `tmeet auth status` 检查状态；未登录时引导用户完成管理端登录。
Agent 不执行全局 npm 安装，不写入 WorkBuddy 或用户默认的 ~/.tmeet。
技能中的脚本以技能目录为基准定位。agent_init.py 为可选元信息初始化；当前 Agent/模型信息未知时跳过。

""" + text[end:]
        skill.write_text(text)
        init_script = dest / "skills/scripts/agent_init.py"
        init_text = init_script.read_text()
        old = "    return Path(DEFAULT_CONFIG_DIR).expanduser()"
        if old not in init_text:
            raise RuntimeError("upstream tmeet initializer changed; review isolation")
        init_script.write_text(init_text.replace(old, '    raise RuntimeError("TMEET_CLI_CONFIG_DIR is required; use the Platform managed config directory")').replace("fallback to ~/.tmeet/.", "required in Platform.").replace("        2. ~/.tmeet/", "        No default directory is written in Platform."))
    if key == "wecom":
        for f in (dest / "skills").rglob("*.md"):
            lines = f.read_text().splitlines(keepends=True)
            text = "".join(line if line.startswith("name:") else re.sub(r"(?<![\w/-])(wecomcli-[a-z0-9-]+)(?:/SKILL\.md)?", lambda m: "@connectors/wecom/skills/" + m.group(1) + "/SKILL.md", line) for line in lines)
            f.write_text(text)
        shared = dest / "skills/wecomcli-shared/SKILL.md"
        text = shared.read_text().replace("1.1.0", "1.2.0")
        start, end = text.index("## Step 1"), text.index("## 通用输出约束")
        text = text[:start] + """## Platform 前置检查

CLI 由 Platform 管理端准备，版本要求为 1.2.0。执行 `wecom-cli auth show --status`：
- `authorized`：继续业务操作。
- `unauthorized` 或 CLI 未准备：引导用户从 Platform 连接器管理发起登录，打开授权链接并扫码。
包内启动器设置独立的 WECOM_CLI_CONFIG_DIR，凭据与 WorkBuddy、用户默认目录分开保存。
不执行全局 npm 安装。退出登录通过 Platform 管理端，仅清理本连接器的独立凭据。

""" + text[end:]
        shared.write_text(text)
        script = dest / "skills/wecomcli-doc/scripts/build_docx.py"
        text = script.read_text()
        old = '    if not raw:\n        raise RuntimeError(f"环境变量 {env_name} 未设置或为空")'
        new = '''    if not raw:
        # Platform reserves these canonical paths for the current run.
        keys = ("AP_CHAT_DIR", "AP_WORKSPACE_DIR")
        roots = [os.environ[k] for k in keys if os.environ.get(k)]
        if env_name == ENV_READABLE:
            import tempfile
            roots.append(tempfile.gettempdir())
        if not roots:
            raise RuntimeError("Platform Chat/Workspace paths are unavailable")
        raw = json.dumps([{"path": p} for p in roots])'''
        if old not in text:
            raise RuntimeError("upstream document script changed; review adaptation")
        script.write_text(text.replace(old, new))
    if key == "tdx-connector":
        f = dest / "skills/SKILL.md"
        text = f.read_text()
        pos = text.index("## 可用工具总览")
        text = text[:pos] + """## Platform 调用约定

先在连接器管理完成通达信 OAuth 登录。工具由 Platform 的 MCP tools/list 自动发现；
下文工具名称为上游名称，调用时匹配当前工具列表的实际公开名称和参数 schema。
代码块展示工具参数示意，不是可通过 Bash 执行的命令。

""" + text[pos:]
        f.write_text(text)


def build(source_root, output):
    output.mkdir(parents=True, exist_ok=True)
    package_root = output / "packages"
    package_root.mkdir(exist_ok=True)
    checksums = {}
    for key, (name, kind, auth) in SPECS.items():
        source, dest = source_root / key, package_root / key
        if dest.exists():
            shutil.rmtree(dest)
        dest.mkdir()
        manifest = {"id": key, "name": name, "version": "1.0.0", "type": kind, "auth_mode": auth, "description": name + " Platform 连接器"}
        if kind == "mcp":
            component = next(iter(json.loads((source / "mcp.json").read_text())["mcpServers"].values()))
            component["type"] = "streamableHttp"
            # Explicit Platform request budgets in milliseconds; no unit guessing.
            component["timeout"] = 60000 if key == "tencent-docs" else 30000
            manifest["oauth"] = {"discovery": True, "resourceMetadataUrl": "https://docs.qq.com/openapi/mcp/.well-known/oauth-protected-resource" if key == "tencent-docs" else "https://txmcp.tdx.com.cn:3001/.well-known/oauth-protected-resource", "scopes": ["docs:read", "docs:write", "docs:manage"] if key == "tencent-docs" else ["mcp.read", "mcp.write"]}
            write_json(dest / "mcp.json", {"mcpServers": {"main": component}})
        else:
            cli_package(source, dest, key)
        write_json(dest / "connector.json", manifest)
        if (source / "skills").exists():
            if any(p.is_symlink() for p in (source / "skills").rglob("*")):
                raise RuntimeError("source skills contain symlinks")
            shutil.copytree(source / "skills", dest / "skills")
            adapt_skills(dest, key)
        archive = output / (key + ".zip")
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
            for f in sorted(dest.rglob("*")):
                if f.is_file():
                    entry = zipfile.ZipInfo(f.relative_to(dest).as_posix(), (2026, 1, 1, 0, 0, 0))
                    entry.create_system = 3
                    entry.external_attr = (0o100755 if f.stat().st_mode & 0o111 else 0o100644) << 16
                    entry.compress_type = zipfile.ZIP_DEFLATED
                    z.writestr(entry, f.read_bytes())
        checksums[archive.name] = hashlib.sha256(archive.read_bytes()).hexdigest()
    write_json(output / "sha256.json", checksums)
    print(json.dumps({"output": str(output.resolve()), "sha256": checksums}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True, help="WorkBuddy connectors catalog directory")
    parser.add_argument("--output", type=Path, default=Path("build/connectors"))
    args = parser.parse_args()
    build(args.source.expanduser().resolve(), args.output.resolve())
