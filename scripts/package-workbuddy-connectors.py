#!/usr/bin/env python3
"""Build deterministic Platform connector ZIPs from a WorkBuddy catalog.

Only connector definitions, brand icons and skill resources are read. User credentials and
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
    "tmeet": ("腾讯会议", "cli", None),
    "tencent-docs": ("腾讯文档", "mcp", "mcp"),
    "tdx-connector": ("通达信", "mcp", "mcp"),
    "wecom": ("企业微信", "cli", None),
    "feishu": ("飞书", "cli", None),
    "kdocs": ("金山文档", "mcp", "token"),
    "github": ("GitHub", "mcp", "token"),
    "tencent-map": ("腾讯地图", "mcp", "token"),
    "westock-mcp": ("腾讯自选股", "mcp", "mcp"),
    "qq-mail": ("QQ 邮箱", "mcp", "mcp"),
}

CLI_PACKAGES = {
    "tmeet": ("@tencentcloud/tmeet", "1.0.16", "scripts/tmeet.js", "tmeet", "TMEET_CLI_CONFIG_DIR"),
    "wecom": ("@wecom/cli", "1.2.0", "bin/wecom.js", "wecom-cli", "WECOM_CLI_CONFIG_DIR"),
    "feishu": ("@larksuite/cli", "1.0.94", "scripts/run.js", "lark-cli", "LARKSUITE_CLI_CONFIG_DIR"),
}

OAUTH = {
    "tencent-docs": {"resourceMetadataUrl": "https://docs.qq.com/openapi/mcp/.well-known/oauth-protected-resource", "scopes": ["docs:read", "docs:write", "docs:manage"]},
    "tdx-connector": {"resourceMetadataUrl": "https://txmcp.tdx.com.cn:3001/.well-known/oauth-protected-resource", "scopes": ["mcp.read", "mcp.write"]},
    "westock-mcp": {"resourceMetadataUrl": "https://stockbuddy.qq.com/.well-known/oauth-protected-resource", "scopes": ["read", "write"]},
    "qq-mail": {"resourceMetadataUrl": "https://api.mail.qq.com/.well-known/oauth-protected-resource", "resource": "https://api.mail.qq.com", "scopes": ["alias:read", "mail:read", "mail:send", "mail:delete"]},
}

TOKEN_FIELDS = {
    "github": ("GITHUB_PERSONAL_ACCESS_TOKEN", "GitHub Personal Access Token", "https://github.com/settings/personal-access-tokens"),
    "kdocs": ("KINGSOFT_DOCS_TOKEN", "金山文档 MCP Token", "https://www.kdocs.cn/latest"),
    "tencent-map": ("TENCENT_MAP_KEY", "腾讯地图 WebService Key", "https://lbs.qq.com/dev/console/quick-register"),
}

NOTES = {
    "tmeet": "保留原始 npm 安装命令；Platform 在独立目录安装固定版本，CLI 发起浏览器授权。",
    "wecom": "保留原始 npm 安装命令；Platform 在独立目录安装固定版本，CLI 发起企业微信扫码。退出仅清理本部署独立凭据。",
    "feishu": "安装 npm 引导包后，官方 scripts/run.js 按宿主平台下载并校验真实 CLI。授权依次完成应用配置与用户登录，保留 auth 数组、skipIf 和 statusMatchJson。需要宿主 Node.js、npm 及官方安装器所需的下载/解压工具。",
    "tencent-docs": "远程 MCP；通过资源元数据发现 OAuth，动态注册、PKCE 与浏览器回调。",
    "tdx-connector": "远程 MCP；通过资源元数据发现 OAuth，动态注册、PKCE 与浏览器回调。",
    "westock-mcp": "远程 MCP；已核对公开资源与授权服务器元数据，实际账号授权仍由用户完成。",
    "qq-mail": "远程 MCP；公开元数据的资源标识为 https://api.mail.qq.com，调用地址为 /mcp。需支持显式 OAuth resource 的新版 Platform。",
    "github": "WorkBuddy 原包依赖 server-side 授权。此包使用 GitHub 官方支持的 PAT Bearer 认证；不复制 WorkBuddy 的服务端客户端身份或登录态。浏览器 OAuth 需另行注册自己的 GitHub App/OAuth App。",
    "kdocs": "WorkBuddy 原包依赖 server-side/wps 授权，此包声明独立的 MCP Bearer Token。保留原 SkillHub 服务和技能版本，来源标识改为 agent-platform。WorkBuddy 的登录代理不在 ZIP 内，不能据此宣称支持相同的一键登录。",
    "tencent-map": "保留 Key 认证，改用腾讯地图官方提供的 Streamable HTTP /mcp 地址；原始 SSE 配置保存在 upstream/mcp.json。Key 只在请求时注入查询参数，不写入清单。",
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


def connector_version(entry):
    version = entry.get("version")
    if version is None or version == "":
        return "0.1.0"
    if not isinstance(version, str) or version != version.strip():
        raise RuntimeError(f"{entry['id']}: invalid upstream connector version")
    return version


def copy_icon(source_root, dest, key):
    icons = source_root.parent / "icons"
    candidates = [icons / (key + extension) for extension in (".svg", ".png")]
    available = [p for p in candidates if p.is_file() and not p.is_symlink()]
    if len(available) != 1:
        raise RuntimeError(f"{key}: expected one SVG or PNG brand icon; found {len(available)}")
    icon = available[0]
    if not 0 < icon.stat().st_size <= 256 * 1024:
        raise RuntimeError(f"{key}: brand icon exceeds the Platform size limit")
    assets = dest / "assets"
    assets.mkdir()
    target = assets / ("icon" + icon.suffix)
    shutil.copyfile(icon, target)
    return target.relative_to(dest).as_posix()


def cli_package(source, dest, key):
    cli = json.loads((source / "cli.json").read_text())
    package, version, entry, command, config_env = CLI_PACKAGES[key]
    cli["platform"] = {
        "npmPackage": package,
        "npmVersion": version,
        "entry": entry,
        "command": command,
        "configEnv": config_env,
    }
    # Retain upstream init as installation metadata. Platform uses the pinned
    # package-local npm runner, not the global shell command in this field.
    if key == "feishu":
        cli["platform"]["nativeEntry"] = "bin/lark-cli"
    if key == "wecom":
        cli["platform"]["logoutMode"] = "delete-config"
        cli["unAuth"] = {"darwin": "", "linux": "", "win32": ""}
        cli["status"] = {os: f"{command}{'.cmd' if os == 'win32' else ''} auth show --status" for os in ("darwin", "linux", "win32")}
        cli["statusMatch"] = r"(?m)^authorized\s*$"
    elif key == "tmeet":
        cli["statusMatch"] = r"(?m)^Logged in\b"
    cli.pop("authQrModal", None)
    write_json(dest / "cli.json", cli)
    bindir = dest / "bin"
    bindir.mkdir()
    (bindir / "launcher.cjs").write_text(LAUNCHER)
    (bindir / command).write_text('#!/bin/sh\nexec node "$(dirname "$0")/launcher.cjs" "$@"\n')
    (bindir / command).chmod(0o755)
    (bindir / (command + ".cmd")).write_bytes(b'@echo off\r\nnode "%~dp0launcher.cjs" %*\r\n')


def adapt_skills(dest, key):
    if key == "feishu":
        shared = dest / "skills/lark-shared/SKILL.md"
        text = shared.read_text()
        marker = "# lark-cli 共享规则"
        if marker not in text:
            raise RuntimeError("upstream Feishu shared skill changed; review adaptation")
        text = text.replace(marker, marker + """

## Platform 安装与授权

本连接器 ZIP 只包含安装定义、启动器和技能，未包含真实 lark-cli。
首次使用从 Platform 连接器管理发起登录，依次完成应用配置授权和用户登录授权。
Platform 安装固定版本 npm 引导包，官方入口按宿主平台下载并校验真实 CLI；不要在 Agent 中全局安装或自行升级。
包内启动器设置独立的 LARKSUITE_CLI_CONFIG_DIR。查询身份用 `lark-cli auth status`，只有 user 身份表示用户登录完成。
下面的权限、身份与命令参考仍适用；管理端显示授权链接时，由用户完成授权。
""", 1)
        shared.write_text(text)
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


def archive_files(archive, root, files):
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for f in sorted(files):
            entry = zipfile.ZipInfo(f.relative_to(root).as_posix(), (2026, 1, 1, 0, 0, 0))
            entry.create_system = 3
            entry.external_attr = (0o100755 if f.stat().st_mode & 0o111 else 0o100644) << 16
            entry.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(entry, f.read_bytes())


def build(source_root, output):
    output.mkdir(parents=True, exist_ok=True)
    package_root = output / "packages"
    package_root.mkdir(exist_ok=True)
    checksums = {}
    index = []
    catalog = json.loads((source_root.parent / ".codebuddy-connector/connectors.json").read_text())
    upstream_entries = {entry["id"]: entry for entry in catalog["connectors"]}
    versions = {key: connector_version(upstream_entries[key]) for key in SPECS}
    for key, (name, kind, auth) in SPECS.items():
        source, dest = source_root / key, package_root / key
        if dest.exists():
            shutil.rmtree(dest)
        dest.mkdir()
        manifest = {"id": key, "name": name, "version": versions[key], "type": kind, "auth_mode": auth, "description": name + ("：CLI 安装定义、启动器与技能（不含真实 CLI）" if kind == "cli" else "：远程 MCP 服务定义"), "icon": copy_icon(source_root, dest, key)}
        if key == "wecom":
            manifest["auth_browser"] = "embedded"
        upstream = dest / "upstream"
        upstream.mkdir()
        write_json(upstream / "catalog-entry.json", upstream_entries[key])
        for file in ("cli.json", "mcp.json", "token-schema.json"):
            if (source / file).is_file():
                shutil.copyfile(source / file, upstream / file)
        if kind == "mcp":
            servers = json.loads((source / "mcp.json").read_text())["mcpServers"]
            if len(servers) != 1:
                raise RuntimeError(f"{key}: expected one upstream MCP component; review conversion")
            component = next(iter(servers.values()))
            component["type"] = "streamableHttp"
            # Explicit Platform request budgets in milliseconds; no unit guessing.
            component["timeout"] = 60000 if key in ("tencent-docs", "kdocs", "github", "qq-mail") else 30000
            if key in OAUTH:
                manifest["oauth"] = {"discovery": True, **OAUTH[key]}
            if key in TOKEN_FIELDS:
                token_key, label, doc_url = TOKEN_FIELDS[key]
                manifest["token_schema"] = {"title": name + "凭据", "description": "仅保存于当前 Platform 部署的独立凭据目录，不包含在 ZIP 中。", "docUrl": doc_url, "fields": [{"key": token_key, "label": label, "type": "password", "required": True}]}
                if key == "tencent-map":
                    # Official equivalent transport, not an SSE endpoint relabeled HTTP.
                    component["url"] = "https://mcp.map.qq.com/mcp?key=${TENCENT_MAP_KEY}&format=0"
                else:
                    component.setdefault("headers", {})["Authorization"] = "Bearer ${" + token_key + "}"
                write_json(dest / "credentials.example.json", {token_key: ""})
            if key == "kdocs":
                component["staticHeaders"]["X-Request-Source"] = "agent-platform"
            write_json(dest / "mcp.json", {"mcpServers": {"main": component}})
        else:
            cli_package(source, dest, key)
        write_json(dest / "connector.json", manifest)
        skills = source / "skills"
        if not skills.exists() and (source / "skill").exists():
            skills = source / "skill"
        if skills.exists():
            if skills.is_symlink() or any(p.is_symlink() for p in skills.rglob("*")):
                raise RuntimeError("source skills contain symlinks")
            shutil.copytree(skills, dest / "skills", ignore=shutil.ignore_patterns(".DS_Store", "__pycache__", "*.pyc"))
            adapt_skills(dest, key)
        skill_count = sum(1 for _ in (dest / "skills").rglob("SKILL.md"))
        info = {"schemaVersion": 1, "id": key, "name": name, "file": key + ".zip", "componentType": kind, "authMode": auth, "cliDelivery": "install-command" if kind == "cli" else "not-applicable", "bundledCLI": False, "launcherIncluded": kind == "cli", "skillCount": skill_count, "notes": NOTES[key]}
        info["icon"] = manifest["icon"]
        info["version"] = manifest["version"]
        if kind == "cli":
            info["npmPackage"], info["npmVersion"] = CLI_PACKAGES[key][:2]
            info["cliPayload"] = "native-download" if key == "feishu" else "npm-javascript"
        write_json(dest / "package-info.json", info)
        readme = f"# {name} Platform 连接器\n\n{NOTES[key]}\n\n"
        readme += f"连接器版本：`{manifest['version']}`。优先原样使用 WorkBuddy 清单版本，未声明时使用约定的 `0.1.0`；Platform 适配或补图标不自行递增版本。\n\n"
        readme += "## 包内容\n\n- `connector.json`：Platform 清单。\n"
        readme += f"- `{kind}.json`：组件及认证/安装定义。\n- `skills/`：{skill_count} 个技能及完整资源（无技能时不生成）。\n- `upstream/`：原始组件配置与目录元信息，供核对；不参与运行。\n- `package-info.json`：分发分类，供人和打包工具读取，不是另一套运行配置。\n\n"
        readme += f"- `{manifest['icon']}`：WorkBuddy 原始品牌图标，由 connector.json 的 icon 字段引用，随 ZIP 安装。需要支持图标字段和图标资源接口的新版 Platform/客户端。\n\n"
        if kind == "cli":
            readme += "## CLI 分发方式\n\n这是 **安装命令型**，ZIP 不含真实 CLI、node_modules、Node.js 或已下载的原生二进制。`bin/` 只有 Platform 启动器，不能据此判断已内置 CLI。\n\n`cli.json.init` 原样保留 WorkBuddy 的安装命令；Platform 使用 `cli.json.platform` 的固定包版本在 `.state/connectors/<id>/npm` 安装，不执行全局 init。安装和授权是两个阶段，导入成功不等于安装或登录成功。\n\n"
        else:
            readme += "## MCP 分发方式\n\n这是 **远程服务定义型**，没有 CLI，也不包含远程 MCP 服务端实现。首次使用需要配置独立凭据或完成浏览器授权。\n\n"
        if auth == "token":
            readme += "通过支持 Token 表单的新版 Platform 填写凭据。`credentials.example.json` 仅列出字段，空值不是有效凭据；不要将真实值写回 ZIP。\n\n"
        readme += f"## 安装与挂载\n\n在 Platform 连接器管理中导入 `{key}.zip`，或者执行：\n\n```bash\nagent-platform connector-manage import --runtime-dir /absolute/runtime /path/to/{key}.zip\n```\n\nAgent 配置示例（与已有 connectors 合并）：\n\n```yaml\nconnectorConfig:\n  connectors:\n    - {key}\n```\n\n连接器技能自动导入，不写入 `skillConfig.skills`。更新同名包需显式覆盖。本包需要支持多步 CLI、Token 查询参数和显式 MCP OAuth resource 的新版 Platform；真实账号权限及完整业务调用需要登录后验证。\n"
        (dest / "README.md").write_text(readme)
        archive = output / (key + ".zip")
        archive_files(archive, dest, [f for f in dest.rglob("*") if f.is_file()])
        checksums[archive.name] = hashlib.sha256(archive.read_bytes()).hexdigest()
        index.append(info)
    write_json(output / "sha256.json", checksums)
    write_json(output / "index.json", index)
    table = "\n".join(f"| {i['name']} | `{i['file']}` | {i['version']} | {'安装命令 + 启动器' if i['componentType'] == 'cli' else '远程 MCP 定义'} | {i['skillCount']} |" for i in index)
    (output / "README.md").write_text(
        "# Platform 连接器 ZIP\n\n"
        "共 10 个可单独导入的连接器，版本遵循 WorkBuddy 清单，未声明时使用约定的 `0.1.0`。"
        "Platform 适配或补图标不自行递增版本。每包均包含 WorkBuddy 原始品牌图标："
        "`assets/icon.svg` 或 `assets/icon.png`，由 `connector.json.icon` 引用。"
        "请先更新至支持图标字段的 Platform 和客户端，再覆盖导入旧包。"
        "`workbuddy-platform-connectors.zip` 是分发合集，请先解压，再逐个导入内部 ZIP，"
        "不能把合集作为一个连接器导入。\n\n"
        "| 连接器 | ZIP | 版本 | 分发方式 | 技能数 |\n| --- | --- | --- | --- | --- |\n" + table + "\n\n"
        "腾讯会议、企业微信和飞书都保留 `cli.json.init` 安装命令；三个包均未内置真实 CLI。"
        "原来腾讯会议和企业微信包里的 bin 也只是启动器。飞书的 npm 包还会按宿主平台下载原生 CLI。"
        "其余七个包只定义远程 MCP 服务，不需要安装 CLI。\n\n"
        "`package-info.json` / `index.json` 的 cliDelivery、bundledCLI、launcherIncluded 专门区分分发方式，"
        "与 connector.json 的认证类型分开。自带真实 CLI 的 builtin.dbx/builtin.httpx 属于 bundled 类型，"
        "但不在这批 WorkBuddy ZIP 内。\n\n"
        "GitHub 改用 PAT，金山文档改用独立 MCP Token；WorkBuddy 的 server-side 授权代理不属于可搬运的包。"
        "其他授权差异、原配置、安装要求见各包 README.md 和 upstream/。包中没有用户凭据或已登录状态。\n"
    )
    archive_files(output / "workbuddy-platform-connectors.zip", output, [output / name for name in checksums] + [output / "README.md", output / "index.json", output / "sha256.json"])
    print(json.dumps({"output": str(output.resolve()), "sha256": checksums}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True, help="WorkBuddy connectors catalog directory")
    parser.add_argument("--output", type=Path, default=Path("build/connectors"))
    args = parser.parse_args()
    build(args.source.expanduser().resolve(), args.output.resolve())
