LaxCode/
├── cmd/
│   └── main/
│       └── main.go          # 程序入口
├── internal/
│   ├── engine/              # MainLoop 核心实现
│   ├── provider/            # 大模型接口抽象与具体厂商 SDK 实现
│   ├── context/             # Token 监控、Prompt 动态组装
│   ├── tools/               # 工具注册表、Middleware、基础极简工具(bash/edit等)
│   ├── memory/              # 基于文件系统的记忆状态存取
│   └── feishu/              # 飞书机器人交互回调
├── go.mod
└── README.md

## Main Loop (核心循环 / ReAct 引擎)

引擎采用 ReAct（Reasoning + Acting）模式驱动任务处理：接收用户任务后进入主循环，
在「推理 → 决策 → 行动 → 观察」之间往复，直到模型宣告任务结束。

```mermaid
flowchart TD
    start([•]) --> task["接收用户任务"]
    task --> loop

    subgraph loop["The Main Loop (核心循环 / ReAct 引擎)"]
        direction TB
        init["初始化 Context"] -->|"1. 整理上下文与可用工具"| prompt["组装 Prompt"]
        prompt -->|"2. 发起 API 推理请求"| model["调用大模型"]
        model -->|"3. 解析 Response"| parse["解析模型返回"]
        parse --> judge{"判断是否调用工具"}

        judge -->|"是 (Model Action)"| tool["执行工具"]
        tool --> observe["追加观察结果"]
        observe -->|"4. 记录 Observation · 开启下一轮 Turn"| prompt
    end

    judge -->|"否 (仅返回纯文本，宣告任务结束)"| final["返回最终结果"]
    final --> endNode([•])

    classDef node fill:#E6E6FF,stroke:#7A6BFF,stroke-width:2px,color:#333
    class init,prompt,model,parse,tool,observe,final node
    classDef judgeStyle fill:#FFF3E0,stroke:#FF9800,stroke-width:2px,color:#333
    class judge judgeStyle
    style start fill:#111,stroke:#111,color:#fff
    style endNode fill:#7A6BFF,stroke:#7A6BFF,color:#fff
    style loop fill:#F5F5FF,stroke:#7A6BFF,stroke-width:2px,stroke-dasharray:6 4
```

> 说明：该图使用 Mermaid 语法，GitHub、GitLab、VS Code 等平台均可直接渲染。