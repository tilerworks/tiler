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

## 使用

目标工作流是：构建镜像、挂载、开发、结束 session、查看状态。快速
开始覆盖其中已可用的部分：

```bash
tiler build --image alice/app dir   # 把准备好的目录打成镜像
tiler mount alice/app work          # 在镜像上挂出可写视图
# 在 work 内开发；改动都落在 upper 层
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
- `tiler doctor` 探测 `/dev/fuse` 与外部工具。
- `tiler build [--full|--incr] [--image owner/repo] [dir]` 把准备好
  的目录打成全量镜像；位于可写合并视图内时，默认把 upper 追加为
  一个增量层。普通目录默认全量构建，必须显式给出 `owner/repo`
  名字；可写合并视图默认增量构建，并从挂载的镜像反查名字。构建
  成功打印一行：`full <owner>/<repo> <id>` 或
  `incr <owner>/<repo> <id>`。
- `tiler images ls [owner/repo] [--archives]` 列出每个活跃镜像与
  链长。给出名字则列出每个链 id，`--archives` 追加归档与 hold 中
  的链 id。
- `tiler images gc` 把无引用的 hold 补归档，再按 `--keep-versions`
  与 `--max-age` 删除过期归档。为 0 表示该项不限制。同一趟回收
  一并清理 session 记录：closed 且完成时间超过 `--session-max-age`
  的记录被删除；active 记录永不自动删除，但挂载全部失活的记录
  会报告 stale；开启 `session_autoclose_stale`（默认关）后，无挂
  载且超龄的 active 记录自动转 closed，进入之后的删除范围。
- `tiler images rm <owner/repo[/archives/<id>]>` 第一次调用标记
  废弃，第二次物理删除。仍有引用时两次都拒绝。
- `tiler mount <image> <dir>` 把镜像供给为工作区。默认视图可
  写：每个 squashfs 成员先只读挂到 `~/.tiler/runtime/` 下的转
  接点，再由 `fuse-overlayfs` 叠加在按挂载点（设备号,
  inode）命名的 upper 之上；同一目录重复挂载是幂等的，并复用
  先前的改动。同一视图的健康挂载提示 `already mounted`；身份
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
  `auto-<时间戳>-<短随机>` session。挂载失败时动作以 failed 状态
  留在记录里，之后 `session open` 会重试。
- `tiler umount <dir>` 按路径卸载视图：先卸合并点，再逆序卸
  运行时转接点。每个点 `fusermount3 -u` 失败即转惰性 `-uz`
  兜底，随后按挂载点（设备号, inode）对应的运行时目录扫表，
  清理表内已不再关联目标的 lower 残留。命令幂等：未挂载的
  `<dir>` 静默成功。upper 与 work 数据从不删除，同一目录之后
  再次挂载时复用先前的改动。
- `tiler session` 管理崩溃恢复据以重放的挂载 session。`session
  new <name>` 在 `~/.tiler/sessions/` 下创建空记录，重名报错。
  `session open <name>` 按挂载顺序遍历记录：健康视图跳过，失活
  视图先清理（`fusermount3 -u`，失败转惰性 `-uz`）再重建，因此
  重复 open 幂等，崩溃后一条 open 恢复全部挂载。`session close
  <name>` 逆序卸载每个记录在案的视图，随后置 closed 并保留记录
  供审计。`session remove <name>` 删除 closed 记录，active 拒绝。
  `session list` 每条记录打印一行，`session show <name>` 展示完整
  动作流水。隐式 `auto-*` session 与显式 session 接受同一套命令。

规划中、尚未提供的命令：

- 查看状态

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

## 许可证

Copyright 2026 Jiang Xin.

本项目以 [MIT](LICENSE) 许可发布。许可证正文为 MIT License。
