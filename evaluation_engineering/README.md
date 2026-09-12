# Agent 评测工程

这个目录放了两份可重复执行的代码任务，用来比较 Agent 迭代前后的表现。两次执行应使用相同的仓库版本、任务提示词和运行环境，只改变待评估的 Agent 配置。

## 任务说明

- [01-paged-read-byte-limit.md](01-paged-read-byte-limit.md)：修复分页读取的字节边界问题。主要观察 Agent 能否读懂已有协议、处理边界状态，并用回归测试证明分页过程不丢内容。
- [02-concurrent-history-append.md](02-concurrent-history-append.md)：保证会话历史并发追加的完整性。这个任务更难，适合检查并发设计、锁粒度、资源生命周期和确定性测试能力。

## 评测流程

1. 选择一份任务提示词，记录当前 commit。为优化前、优化后各准备一个干净且互不影响的工作区。
2. 让两个版本的 Agent 分别执行同一份提示词，保留各自的 `history.jsonl`。不要在执行中手动补代码或提示，否则比较会失真。
3. 调用 `.laxcode/skills/evaluate-agent-iteration`，提供四项信息：

   ```text
   优化前 history.jsonl：<path>
   优化后 history.jsonl：<path>
   Agent 执行的任务：<所选 md 的完整内容或准确介绍>
   迭代内容：<本次修改了什么，希望改善什么>
   ```

4. Skill 会先复制并截断会话中的长内容，再比较任务完成度、工具使用、效率和稳定性。原始记录不会被修改。
5. 完整报告写入 `/tmp/laxcode_evaluate/results/`。根据报告里的证据和改进建议调整 Agent，再用同一任务跑下一轮。

如果要比较不同模型、提示词或工具配置，把实际差异写进“迭代内容”。一次最好只改一类变量，结论更容易解释。
