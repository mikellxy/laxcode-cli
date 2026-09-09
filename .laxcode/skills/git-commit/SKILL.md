---
name: git-commit
description: 执行 git commit / 提交代码任务时使用。先读取仓库 .gitmessage（或 commit.template 指向的模板）中约定的提交规范，再按规范生成提交信息并完成提交。Use when 用户要求提交代码、执行 git commit、git add + commit、或审查提交信息格式是否符合仓库规范。
---

# Git Commit Skill

帮助生成符合仓库规范的提交信息并执行提交。**核心原则：提交前先找规范，规范优先于个人习惯。**

## 何时使用

- 用户说"提交一下 / git commit / commit changes"
- 用户要求"用简洁英文描述"等自定义提交信息风格时，仍需先满足仓库 .gitmessage 规范
- 用户要求检查某条提交信息是否符合规范

## 执行步骤

### 1. 了解改动范围（勿漏勿多）

```bash
git status          # 查看工作区/暂存区状态
git diff --stat     # 未暂存改动概览
git diff --cached --stat  # 已暂存改动概览
```

- 确认改动内容后再决定 stage 哪些文件（常用 `git add <file>` 精确添加，或用户授权后 `git add -A`）
- 若改动内容不明，先 `git diff` 阅读具体 diff，不要凭猜测提交

### 2. 读取提交规范（关键步骤）

按以下优先级查找，**禁止跳过**：

```bash
# a) 仓库根目录的 .gitmessage（本项目即采用此方式）
ls .gitmessage

# b) git 配置的 commit.template
git config --get commit.template
```

- 找到 `.gitmessage` 或模板文件后，用 read_file 完整阅读其内容
- 若两者都不存在，退回通用 Conventional Commits 约定：
  `<type>(<scope>): <subject>`，type 取 feat/fix/chore/ci/docs/refactor/test/perf
- 模板中的中文说明只是"给作者看的注释"，提交信息内容按模板要求的语言与格式写

### 3. 依据模板生成提交信息

本仓库 `.gitmessage` 常见要求（以实际读取到的内容为准）：

- **标题**：`<type>(<scope>): <subject>`，≤ 72 字符
  - type 必填：feat | fix | chore | ci | docs | refactor | test | perf
  - scope：受影响的模块/包（尽量对齐 Go 包或 DDD 分层），如 llmprovider、tui
  - subject：英文、祈使句、首字母小写、结尾不加句号
- **正文**（选填）：说明"做了什么与为什么"，不写代码细节；与标题间隔一个空行；每行 ≤ 72 字符
- **页脚**（选填）：`Closes #123` / `Refs #456` / `BREAKING CHANGE: ...`
- **共同作者**（必加）：正文之后（或直接在标题之后，与正文之间空一行）追加一行 trailer，用于把提交归署到组织 bot：
  ```
  Co-Authored-By: laxcode-cli <326837968+laxcode-cli[bot]@users.noreply.github.com>
  ```
  GitHub 只按**邮箱**归署，trailer 前的显示名可自定义（此处用 `laxcode-cli`，不带 `[bot]` 后缀）；邮箱是 GitHub App bot 的 noreply 地址（`ID+name[bot]@users.noreply.github.com`），必须精确匹配，改组织或 bot 时需同步更新。
- 若模板规定了语言（如英文），即便用户说"中文描述"也以模板语言为主；用户附加要求（如"简洁"）在满足模板的前提下体现
- 无论有无正文，`Co-Authored-By: laxcode-cli <...>` 都必须出现在提交信息末尾，独占一行、前后留空行

### 4. 执行提交

- 提交信息包含正文时，用多个 `-m` 分段（标题 / 正文 / 共同作者各一个 `-m`），或用 heredoc 写到临时文件后 `git commit -F`：
  ```bash
  git commit -m "feat(tui): <subject>" -m "<body>" \
    -m "Co-Authored-By: laxcode-cli <326837968+laxcode-cli[bot]@users.noreply.github.com>"
  ```
- **不要把模板里以 `#` 开头的注释行写入提交信息**（git 会自动忽略，但别手动带进去）
- 提交后向用户汇报：commit hash（前 7 位）、改动统计（X files changed, +Y/−Z）

## 注意事项

- `.gitmessage` 只存在于某些仓库根目录；读取前先确认存在，避免误读失败
- 若工作区存在未 stage 的无关改动，先与用户确认是否一并提交
- 提交信息应以仓库模板为最高优先级，模板缺失时才用通用约定
- 不要在 commit 后擅自 push，除非用户明确要求
