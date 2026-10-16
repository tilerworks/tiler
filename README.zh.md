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

目标工作流是：构建镜像、挂载、开发、结束 session、查看状态。

当前可用：

- `tiler version` 打印一行版本。
- `tiler doctor` 探测 `/dev/fuse` 与外部工具。

规划中、尚未提供的命令：

- 构建镜像
- 挂载工作区
- 结束 session
- 查看状态

## 用户故事

开发者用 tiler 快速制备本地环境。新机器挂载预置环境只需数秒。完整
克隆、安装依赖再构建则需要数分钟到数小时。

只读基座保持不可变。上层可以丢弃后重新挂载。

session 隔离各个项目，同时共享同一只读基座。八个 session 共享一个
lower 时，每个 upper 的稳态大约 6.5 MB。

崩溃后可以通过重放动作记录恢复。

## 许可证

Copyright 2026 Jiang Xin.

本项目以 [MIT](LICENSE) 许可发布。许可证正文为 MIT License。
