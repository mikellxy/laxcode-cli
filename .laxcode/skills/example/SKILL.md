---
name: example
description: 示例技能，演示 SKILL.md 的标准写法。Use when 用户询问技能（skill）格式、排查技能为何未出现在索引中，或要创建新技能时，先读取本文件作为参考样板。
---

# Example Skill

这是 LaxCode 的最小可用技能定义样板。

## 何时使用

- 用户询问技能（SKILL.md）怎么写、格式是什么
- 用户创建的技能未出现在技能索引中，需要对照合法样板排查
- 演示技能索引注入 system prompt 的效果

## 技能编写约定

1. 技能目录位于 `.laxcode/skills/<技能名>/`，定义文件必须严格命名为 `SKILL.md`
2. frontmatter 的 `name` 必须与所在目录名完全一致，仅用小写字母、数字与连字符，长度不超过 64
3. `description` 用一句话说明"何时使用本技能"，它会被注入 system prompt 的技能索引
4. 本正文是给大模型的使用指引，技能被选用时经 read_file 工具读取后按内容执行
