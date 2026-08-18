package schema

type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词：确立 Agent 的人格与红线
	RoleUser      Role = "user"      // 用户输入，工具执行返回的结果
	RoleAssistant Role = "assistant" // 模型输出
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}
