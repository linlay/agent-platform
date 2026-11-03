# Shell 命令规则改为 YAML

> 2026-11-03 起草。目标：能完全用数据描述的 Unix 命令，选项与操作数规则写在 YAML 里，新增这类命令或参数只改 YAML，不写 Go。审批策略、档位和兜底规则不变。

## 1. 已定

- 文件：`internal/shellreview/commands/commands.yml`，`go:embed` 编进程序。不放 `configs/`：规则是程序语义，跟代码同版本、同测试，所有部署一致；`configs/` 不新增任何东西，不做部署级覆盖。
- 只有一个文件，不分大类文件。各命令的区别只在“操作数怎么用”，由 `operands` 一个字段表达。
- 一个命令要么整个在 YAML，要么整个在 Go，不拆开。需要条件逻辑或特殊语法的命令（含它的选项表）留在 Go。
- 长、短参数分开写：两者写法不同（短参数可连写 `-lw`、值可紧贴 `-n5`；长参数用 `=`）。
- 用仓库自带的 YAML 子集解析器 `config.LoadYAMLTreeBytes`（内置工具定义也用它），不引入第三方库；`internal/config` 不依赖 shellreview，没有循环依赖。

## 2. 格式

顶层键是命令名。每个命令只有这几个字段：

| 字段 | 必填 | 含义 |
| --- | --- | --- |
| `operands` | 是 | 操作数怎么用：`none`（不是文件）、`read`、`create`、`output`、`delete` |
| `short` | 否 | 短参数，按角色分组，值为字母列表 |
| `long` | 否 | 长参数，按角色分组，值为不带 `--` 的名字列表 |
| `numeric` | 否 | `true` 时接受 `-20` 这种数字写法（head/tail） |
| `examples` | 否 | 样例，见 §4 |

角色与现有 `types.OptionRole` 一一对应，只开放第一批命令用到的九个：

| 角色 | 含义 |
| --- | --- |
| `flag` | 开关，不带值 |
| `value` | 带值，值不是文件；`--x=v`、`--x v`、`-xv`、`-x v` 都可以 |
| `optional` | 值只能紧贴：`--x=v`、`-x5`；不会吞掉下一个参数 |
| `read` / `write` | 值是要读 / 写的文件 |
| `read-dir` / `write-dir` | 值是要读 / 写的目录 |
| `exec` | 值是要运行的程序，按执行处理 |
| `recursive` | 出现即表示操作数按递归处理；`delete` 加递归算破坏性 |

```yaml
wc:
  operands: read
  short:
    flag: [c, l, L, m, w]
  long:
    flag: [lines, words, chars, bytes, max-line-length]
    optional: [total]
    read: [files0-from]

head:
  operands: read
  numeric: true
  short:
    flag: [q, v, z]
    value: [n, c]
  long: …

rm:
  operands: delete
  short:
    flag: [f, i, I, d, v]
    recursive: [r, R]
  long:
    flag: [force, dir, verbose, one-file-system, preserve-root]
    optional: [interactive]
    recursive: [recursive]
```

没登记的参数沿用现有兜底：所有操作数升级为写入，再加一项复杂命令审批。不为 YAML 命令另设兜底规则。

## 3. 第一批范围

进 YAML（28 个，均为现有 Go 表原样搬迁）：

| operands | 命令 |
| --- | --- |
| none | pwd、date、sleep、uname、whoami、id、basename、dirname、which、printenv、tr、free |
| read | cat、head、tail、wc、file、stat、readlink、realpath、cut、sort、diff |
| create / output / delete | mkdir、touch / tee / rmdir、rm |

留在 Go，以及原因：

| 命令 | 原因 |
| --- | --- |
| echo、true、false、printf、test、cd、top | 参数是数据、格式串或谓词语法；top 带参数按执行处理 |
| ls、du | 没有操作数时读取当前目录 |
| grep、rg | 第一个操作数是匹配模式，除非用了 `-e`/`-f` |
| uniq | 第二个操作数是输出文件 |
| chmod | 第一个操作数是权限模式，除非用了 `--reference` |
| git | 读写判断依赖条件：`-C` 改仓库位置、branch/stash/config 按操作数区分只读、checkout 带 `.` 才是破坏性、push `+refspec`、会执行程序的配置项 |
| sed、awk、find、tar、jq、curl、wget、cp、mv、ln、pdftotext、lsof | 内嵌语言、表达式或源/目标语义 |

## 4. 校验与测试

- 加载严格：未知字段、未知角色、未知 `operands` 值、列表项不是字符串都直接报错。数字字母要加引号（`flag: ["0", "1"]`），否则会被解析成数字。
- 同一个命令同时出现在 YAML 和 Go 分析器里，直接报错。命令名必须已在 `registry` 中，且是普通命令（可以是 builtin，如 pwd）；包装器、解释器、构建工具拒绝。
- 文件是内嵌的，加载在包初始化时完成；出错即 panic，测试阶段就会暴露，生产不会遇到与测试不同的文件。
- 通用测试：对每个命令自动追加一个未登记参数，确认仍判为“不认识”，不需要每个命令手写反例。
- `examples` 可选，用于新增命令里容易写错的参数（尤其是带值的）。`expect` 的键为 `read`/`create`/`output`/`delete`/`recursive`（路径列表）和 `exec: true`；要写全，漏写或多出都算失败，样例出现未登记参数也算失败。破坏性由“删除 + 递归”自动得出，不用写。

```yaml
sort:
  …
  examples:
    - args: [-o, out.txt, in.txt]
      expect: {read: [in.txt], output: [out.txt]}
```

- 第一批是纯搬迁：`TestBashReviewNormalizedBaseline`（387 行快照）和现有分析测试必须零差异，快照不更新。哪个命令出现差异，就留在 Go，不为它扩展格式。

## 5. 批次

| 批次 | 内容 | 完成门槛 |
| --- | --- | --- |
| Y1 格式与加载（已完成，未提交） | `commands.yml` 结构、严格加载、通用测试、`examples` 执行；先只搬 `wc` 一个命令打通。五种 `operands` 与九个角色由测试内的假命令全部覆盖 | 快照零差异 |
| Y2 搬迁 | 其余 27 个命令搬进 YAML；同一提交删除对应的 Go 表（`simpleSpecs` 条目、`*Spec` 变量和 `analyzers` 条目），不留转发 | 快照与现有测试零差异 |
| Y3 新增命令 | 允许 YAML 登记 `registry` 里没有的命令名（按名字登记为普通程序，身份识别照常要求系统程序）；之后按实际审批日志逐个补命令 | 每个新命令有样例；三档权限行为符合预期 |

Y1、Y2 只挪结构、不改行为；Y3 才有行为变化，单独评审。

Y2 注意：
- 现有 Go 代码对 `file`、`diff` 的 `-r`/`-R` 一律按递归读取（`file -r` 实际是“不转义输出”）。为保持零差异，`file` 的 `r` 先写在 `recursive` 下，改正另提。
- `sort` 的 `r`/`R` 是排序开关，写在 `flag` 下，现有代码也强制不递归，行为一致。
- 搬完后 `simpleSpecs` 为空，一并删除；`fileOperands`、`fileOperationOperands` 只剩留在 Go 的命令。

## 6. 收尾

Y2 完成后更新：[工具目录权限](../docs/工具目录权限.md)（“命令语义由 `internal/shellreview/commands` 的命令说明表统一描述”一句改为说明 `commands.yml` 与 Go 分析器的分工）、AGENTS.md 的 Shell 审查段（同一处措辞）。Y3 完成后删除本文。

## 7. 不做的

- 部署级覆盖：没有需求；以后要做也只在 `tools.yml` 的 `bash` 段加一节。
- GNU/BSD 分平台写：现有表没有区分，本次保持现状。
- 子命令类（git、npm、docker 的子命令分派）：git 留在 Go；等第一个真正需要的 CLI 出现再设计。
- 单横线单词参数（ffmpeg `-version`、go `-run`）：等第一个需要的命令出现再加。
- Windows cmdlet：参数来自模块元数据，见 [shell-command-analysis](shell-command-analysis.md)；外部程序在 C10 之后可复用 `commands.yml`。
