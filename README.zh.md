# tiler

[English](README.md)

tiler 把目录快照打成只读 squashfs 镜像，再用 overlay 提供可写工作区。
工具不检查内容类型。Git 仓库加速是旁边脚本完成的一种使用场景。

以 [MIT](LICENSE) 许可发布。

## 安装

在本仓库执行 `make build` 即可构建。静态二进制写到 `bin/tiler`。

`go install` 尚在规划中。发布二进制下载尚在规划中。

`tiler doctor` 检查这些运行时工具：

- `squashfuse_ll`
- `fuse-overlayfs`
- `fusermount3`
- `mksquashfs`

`git` 可选。doctor 总会列出它，缺少该二进制是一次失败的探测，而不是静默跳过。git 探测失败不会阻断其他检查。

可选辅助工具以源码形式放在 `contrib/` 目录：`concatfs`（字节分块
拼接视图路线；构建需要 gcc 与 libfuse3-dev）与 `slowfs`（延迟注入，
用于慢速 NAS 对比；构建需要 Go 工具链）。每个目录都记录上游来源、
许可与构建命令。场景需要而工具缺失时 tiler 会提示安装；tiler 从不
自动安装它们。

## 使用

目标工作流是：构建镜像、挂载、开发、结束 session、查看状态。快速
开始覆盖整个工作流：

```bash
tiler build --image alice/app dir   # 把准备好的目录打成镜像
tiler mount alice/app work          # 在镜像上挂出可写视图
# 在 work 内开发；改动都落在 upper 层
tiler status work                   # 覆盖该路径的 session 与其健康
tiler umount work                   # upper 与 work 数据保留
```

session 把多个挂载归入一份持久记录，是崩溃恢复的单位：

```bash
tiler session new feature                  # 持久记录
tiler mount --session feature alice/app work
tiler mount --session feature bob/tools tools
tiler session open feature                 # 崩溃之后：重放恢复
tiler session close feature                # 逆序卸载，记录保留
```

当前可用：

- `tiler version` 打印一行版本。
- `tiler doctor [--json]` 探测 `/dev/fuse`、外部工具与 squashfuse
  subdir 基线，并进一步体检存储：稀疏阶梯探测镜像库与缓存目录
  所在文件系统的单文件大小限制，发现限制且未启用分块时建议开启
  分块存储；有界性能探测对比两个目录的冷读顺序/随机延迟与吞吐，
  并据此给出缓存建议——建议中的延迟基线只是趋势参考，不是
  SLA；可选的 contrib 辅助工具 `concatfs` 与 `slowfs` 一并探测，
  缺失时输出构建指引，tiler 从不自动安装。存在 fail 探测项即
  退出码 1；warn 与 info 不影响退出码。
- `tiler build [--full|--incr] [--image owner/repo] [dir]` 把准备好
  的目录打成全量镜像；位于可写合并视图内时，默认把 upper 追加为
  一个增量层。普通目录默认全量构建，必须显式给出 `owner/repo`
  名字；可写合并视图默认增量构建，并从挂载的镜像反查名字。构建
  成功打印一行：`full <owner>/<repo> <id>` 或
  `incr <owner>/<repo> <id>`。启用分块存储后，全量构建按所选分块
  形态入库，而不再是单个 `image.sqsh`（见下文「分块存储」）；
  split 开启期间增量构建被拒绝——分块形态只覆盖全量镜像。
- `tiler images ls [owner/repo] [--archives] [--json]` 列出每个
  活跃镜像与链长。给出名字则列出每个链 id，`--archives` 追加
  归档与 hold 中的链 id。分块活跃位追加 `form=blocks` 或
  `form=members` 标记；单名列表随之输出 `blocks <n>` 行或逐分区
  一行 `member <name>`。`--json` 改为渲染一份机器可读文档，分块
  活跃位新增 `form` 与 `members` 字段（契约只增不改）。
- `tiler images gc` 把无引用的 hold 补归档，再按 `--keep-versions`
  与 `--max-age` 删除过期归档。为 0 表示该项不限制。同一趟回收
  一并清理 session 记录：closed 且完成时间超过 `--session-max-age`
  的记录被删除；active 记录永不自动删除，但挂载全部失活的记录
  会报告 stale；开启 `session_autoclose_stale`（默认关）后，无挂
  载且超龄的 active 记录自动转 closed，进入之后的删除范围。
  唯一的例外是视图全部失活的 `--rm` active 记录：无论年龄几何，
  都会被拆除视图、清理 runtime 并删除记录。
- `tiler images rm <owner/repo[/archives/<id>]>` 第一次调用标记
  废弃，第二次物理删除。仍有引用时两次都拒绝。
- `tiler mount <image> <dir>` 把镜像供给为工作区。默认视图可
  写：每个 squashfs 成员先只读挂到 `~/.tiler/runtime/` 下的转
  接点，再由 `fuse-overlayfs` 叠加在按挂载点（设备号,
  inode）命名的 upper 之上；同一目录重复挂载是幂等的，并复用
  先前的改动。分块载荷形态经同一引用解析，按各自的路线挂载
  （见下文「分块存储」）。同一视图的健康挂载提示 `already
  mounted`；身份
  不一致报冲突错误，绝不静默重挂。`--read-only` 把单镜像直接
  挂在 `<dir>`，带增量链时仅以 lowerdir 合并。`<image>` 是单
  个 squashfs 文件路径，或 `owner/repo` 库引用：库引用经活跃
  位 meta 解析为全量镜像加增量链，一个栈至多八个成员；参数同
  时是合法名字与真实文件时，库引用优先。库成员默认先做 size
  加 mtime 快速校验；`--verify` 改为对每个记录在案的 sha256
  摘要整体重验（文件路径形态没有记录摘要）。`--subdir <path>`
  把镜像内一个子目录暴露为挂载根：缺少该子目录的成员不进栈，
  全部缺失时挂载失败且不创建任何东西。子树内没有 `.git` 时，
  工作区 git 命令会向上寻到挂载点之外，因此 subdir 形态面向
  独立子仓库或非 git 内容。配置 `cache_enabled`（默认关；
  `--cache` 对单次调用开启）后，每个成员先按内容摘要拷入
  `cache_dir` 并整体校验通过才挂载，NAS 上的镜像因此从本地
  副本挂载，而挂载表身份仍指向原始库路径。目标必须是当前用
  户拥有的空目录，且位于运行时树与全部保留目录之外。每次挂载
  都在引擎执行前先写进 session 记录（write-ahead）：`--session
  <name>` 挂进用 `session new` 创建的 session，环境变量
  `TILER_SESSION_ID` 绑定一个，两者都没有时自动创建隐式的
  `auto-<时间戳>-<短随机>` session。`--rm` 把这个隐式 session
  标记为待删：最后一个视图卸载后，记录被删除、runtime 目录被
  清理，upper 数据一并抹除。它不能与 `--session` 或
  `TILER_SESSION_ID` 同用——命名 session 请用 `session new --rm
  <name>`。挂载失败时动作以 failed 状态留在记录里，之后
  `session open` 会重试。
- `tiler umount <dir>` 按路径卸载视图：先卸合并点，再逆序卸
  运行时转接点。每个点 `fusermount3 -u` 失败即转惰性 `-uz`
  兜底，随后按挂载点（设备号, inode）对应的运行时目录扫表，
  清理表内已不再关联目标的 lower 残留。命令幂等：未挂载的
  `<dir>` 静默成功。upper 与 work 数据从不删除，同一目录之后
  再次挂载时复用先前的改动。
- `tiler session` 管理崩溃恢复据以重放的挂载 session。`session
  new <name>` 在 `~/.tiler/sessions/` 下创建空记录，重名报错；
  `--rm` 标记的记录在 `session close` 时删除记录并清理每个视图
  的 runtime，而不再保留供审计。
  `session open <name>` 按挂载顺序遍历记录：健康视图跳过，失活
  视图先清理（`fusermount3 -u`，失败转惰性 `-uz`）再重建，因此
  重复 open 幂等，崩溃后一条 open 恢复全部挂载。closed session
  同样借此恢复：视图重新挂载，记录回到 active。`session close
  <name>` 逆序卸载每个记录在案的视图，随后置 closed 并保留记录
  供审计——但 `--rm` session 例外，它被直接删除：记录被删、每个
  视图的 runtime 目录被清理（upper 数据一并抹除），命令打印
  `removed` 而非 `closed`。`session remove <name>` 删除 closed
  记录，active 拒绝。
  `session list [--json]` 每条记录打印一行，`session show <name>
  [--json]` 展示完整动作流水，show 的 `--json` 形态就是记录对象
  本身。隐式 `auto-*` session 与显式 session 接受同一套命令。
- `tiler status [<path>] [--json]` 把 session 记录与真实挂载状态
  对账。不带路径时每条记录输出一块——每个未卸载的挂载动作标注
  `healthy` 或 `stale`——`TILER_SESSION_ID` 命名的 session 标记
  `current`；挂载表中存在但无 active 记录认领的 tiler 视图列为
  `orphan` 行。带路径时报告覆盖该路径的 session：合并视图内的
  深层路径与运行时树内路径（upper 或 lower 转接点）都能反查到
  覆盖 session；无 session 覆盖的路径报错。

### 分块存储

分块存储是面向单文件大小受限存储的可选形态——配额型 NAS 共享、
有 4 GiB 单文件上限的 FAT 类文件系统。形态完全由配置显式选择，
不做运行期自动切换，默认关闭：

```bash
split_enabled: true          # --split
split_mode: blocks           # --split-mode blocks|members
split_route: reassemble      # --split-route reassemble|concat（仅 blocks）
```

- **blocks** 把全量镜像存为定长字节块（`blocks/<id>/blk.NNN`），
  清单逐块记录 size、sha256 与 mtime。挂载默认走重组路线：块按
  清单顺序流式拼接进 `<cache_dir>/<整体sha256>.sqsh`——逐块校验
  加整体摘要校验——因此缓存目录需要整图大小的空间。
  `split_route: concat` 改由 contrib 的 `concatfs` 把块目录挂成
  一个虚拟拼接文件：零重组、不落缓存副本，挂载表身份仍是规范
  库路径。两条路线都不再叠加 `cache_enabled` 的二次拷贝。
  `concatfs` 缺失时降级重组直挂，并在 stderr 输出 contrib 构建
  指引；tiler 从不自动安装或现场构建。
- **members** 按顶层目录分区：每个分区是独立完整的 squashfs
  （`members/<name>.sqsh`），整体身份是派生清单摘要。挂载是
  只读子路径森林——每个分区挂在同一父目录的 `<dir>/<name>`
  子路径下——森林是一个挂载组合：session 记录一条动作，
  `status` 按成员挂载标注组合健康，一条 `session open` 逐成员
  幂等重放（健康成员复用、失活成员清理重挂），`umount <父目录>`
  逆序拆除全部成员子挂载。请求可写挂载或 `--subdir` 以 usage
  错误拒绝并给出引导。

doctor 的单文件限制探测在镜像库或缓存目录发现上限时，会建议
启用分块存储；按建议开启后，同一探测报告分块形态，构建即按
所选形态入库。分块形态只覆盖全量镜像，split 开启期间增量链
构建保持拒绝。

列表/查看类命令都带机器可读形态，面向 CI 门禁与 Agent 消费。
schema 稳定、字段只增不改，由 golden 文件测试钉住：

```bash
tiler images ls --json | jq -e '.images | length > 0'    # CI 门禁
tiler status --json | jq -r '.sessions[].name'           # Agent 盘点
tiler doctor --json | jq -r '.results[] | select(.status == "fail") | .name'
```

全部命令共享同一退出码契约：0 成功；1 运行或探测失败；2 usage
错误。

`tiler doctor` 每个探测项输出一行 tab 分隔文本，例如（详情从
略）：

```text
file_limit_images	pass	no single-file size limit below 512 GiB
storage_perf_images	info	seq 112.5 MiB/s in 145.6 ms; rand avg 6.12 ms ...
storage_perf_cache	info	seq 4180.2 MiB/s in 3.9 ms; rand avg 0.05 ms ...
cache_advice	warn	image storage random read averages 6.12 ms, ...
contrib_concatfs	warn	optional tool; sources ship in the contrib/ ...
```

## 配置

tiler 读取分层的 YAML 配置。首次运行时，它依据一份带注释、钉住
内置默认值的模板创建 `$HOME/.tiler.yaml`——这个文件是供你编辑的
起点，而非必需项：HOME 只读时会被容忍，tiler 回退到内置默认；
已存在的文件绝不改写。

```yaml
images_dir: ~/.tiler/images
runtime_dir: ~/.tiler/runtime
sessions_dir: ~/.tiler/sessions
cache_dir: ~/.tiler/cache
log_level: info
```

取值按层解析，后者覆盖前者：内置默认、用户文件
`$HOME/.tiler.yaml`、从工作目录向上遍历发现的项目级 `.tiler.yaml`，
以及命令行 flag。文件内的相对路径以该文件所在目录为基准解析，
`~/` 前缀展开为 `$HOME`。

向上遍历在工作目录位于 `$HOME` 内时止于 `$HOME`，否则止于文件
系统根。`TILER_CEILING_DIR` 可以更早截止：当截止目录是工作目录的
祖先时，它自身的 `.tiler.yaml` 仍生效，但其上一律不读；不是祖先
时忽略。它把配置发现约束在测试沙箱内，或某个不应继承父目录配置
的检出内。

`tiler doctor` 在探测表之前先报告解析后的配置——配置文件位置与
核心目录，每项一行 tab 分隔的 `config` 行：

```text
config	path	/home/alice/.tiler.yaml
config	images_dir	/home/alice/.tiler/images
config	runtime_dir	/home/alice/.tiler/runtime
```

`--json` 形态把同样的数据作为顶层 `config` 对象（`path`、
`images_dir`、`runtime_dir`、`sessions_dir`、`cache_dir`、
`log_level`）与 `results` 并列输出。config 段落不影响 doctor 的
判定结果。

## 用户故事

开发者用 tiler 快速制备本地环境。新机器上挂载本身毫秒级完成，工作
区数秒可用；完整克隆、安装依赖再构建则需要数分钟到数小时。

第一份入库镜像只需一条命令：场景脚本准备好目录，
`tiler build --image alice/app dir` 把它打进 `~/.tiler/images/` 下的
镜像库；同名下一次全量构建会把上一份归档，而不是打断读者。之后
`tiler images gc` 按份数与时长裁剪归档。

只读基座保持不可变。上层可以丢弃后重新挂载。

session 隔离各个项目，同时共享同一只读基座。八个 session 共享一个
lower 时，每个 upper 的稳态大约 6.5 MB。

崩溃可以恢复，因为每个挂载动作都先行落盘（write-ahead）：
`kill -9` 杀掉 tiler 或 FUSE 守护进程后，一条 `tiler session open
<name>` 按记录重放，仍健康的视图跳过，失活的清理后重建，upper
层数据不丢。

大镜像在受限存储上照常工作。目标文件系统有单文件大小上限时
（配额型 NAS 共享、FAT 类卷），`tiler doctor` 报告限制并建议
启用分块存储。开启 `split_enabled` 并选择 `split_mode: blocks`
后，同一份镜像以逐块校验的字节块入库；挂载时在缓存目录重组，
或在 contrib 的 `concatfs` 就位且 `split_route: concat` 时，把
块目录以一个虚拟文件直接供给——完全不重组。顶层目录彼此独立的
源树也可以改选 `split_mode: members` 分区入库，挂成只读子路径
森林。同一个镜像库、同一套命令面，存储形态只是配置选择。

机器读同一套命令面。`images ls`、`session list`、`session show`、
`status` 与 `doctor` 都带 `--json` 形态，schema 稳定、字段只增不
改并由 golden 文件钉住：CI 门禁可以直接断言镜像齐全、没有 stale
挂载，Agent 可以从 doctor 的性能数字决定要不要开启缓存，无需
解析人类文本。

## 许可证

Copyright 2026 Jiang Xin.

本项目以 [MIT](LICENSE) 许可发布。许可证正文为 MIT License。
