## Context

现状：`internal/tools/base.go` 的 WriteFileTool 通过可选 `mode` 参数（`write`/`append`）区分覆写与追加，成功返回 `file written: <path> (mode=<mode>)`。append 使写入结果依赖文件当前状态（非幂等），且与规划中 edit_file 的增量编辑职责重叠——动机详见 proposal.md - Why。

影响面已核实收敛：append 的引用仅存在于 base.go 内部（参数定义、mode 解析、switch 分支、返回值拼接），registry、sysprompt、engine、provider 各层均不感知 mode。

## Goals / Non-Goals

**Goals:**
- write_file 语义收窄为纯全量写入，工具定义与执行行为保持一致
- 用持久化单元测试固化收窄后的行为契约，测试自身不产生任何残留文件

**Non-Goals:**
- 不设计、不实现 edit_file（追加能力由其未来承接，另行立项）
- 不修改历史文档 docs/self-reinforcement/dev_write_file_tool.md
- 不引入 created/overwritten 的返回值区分

## Decisions

### D1: 成功返回值定为「内容成功写入文件：<相对路径>」

去掉 `(mode=write)` 冗余。曾考虑写入前 `os.Stat` 一次以区分 created/overwritten——为保持输出最小化而否决：工具 description 已声明覆写语义，模型可据此推断行为。

### D2: description 定稿为「写入完整文件内容，创建新文件或覆写已有文件」

不在 description 中教模型组合策略（如「修改已有文件请先 read 再覆写」）。组合策略属于模型自主决策，工具契约只声明单工具语义。

### D3: 测试用 t.TempDir() 隔离，框架自动清理

测试将 `env.WorkDir` 指向 `t.TempDir()`，临时目录由测试框架自动回收；全程不在仓库工作目录产生测试产物，无需手动清理。覆盖场景：创建（含自动建父目录）、覆写、路径穿越拦截、绝对路径拦截、缺少必填参数报错。

## Risks / Trade-offs

- [R1 追加能力空窗期] → edit_file 落地前，模型需「read 全文 + write 全量」才能完成追加，大文件 token 成本高 → 缓解：可接受的中间状态，不构成保留 append 的理由
- [R2 历史文档与实现不一致] → dev_write_file_tool.md 记录的 append 行为已不存在，读者可能被误导 → 缓解：该文档定位为历史 transcript，不篡改；本 change 的 proposal/design 即为演进记录

## Migration Plan

单二进制、无持久化状态、无外部消费者，直接替换实现即可。回滚策略：git revert 本 change 对应提交。
