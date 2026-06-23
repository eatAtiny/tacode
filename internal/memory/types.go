package memory

// MemoryEntry 表示一条结构化记忆（对应一个 .md 文件）。
type MemoryEntry struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Type        string   `yaml:"type"`       // user | feedback | project | reference
	Importance  int      `yaml:"importance"`  // 1-5
	Tags        []string `yaml:"tags"`
	Created     string   `yaml:"created"`
	Updated     string   `yaml:"updated"`
	Content     string   // frontmatter 之后的正文
}

// Summary 表示一条对话摘要。
type Summary struct {
	Round     int    `json:"round"`
	Timestamp string `json:"timestamp"`
	Summary   string `json:"summary"`
}

// ExtractionResult 是 LLM 提取记忆的返回结果。
type ExtractionResult struct {
	Summary  string         `json:"summary"`
	Memories []MemoryAction `json:"memories"`
}

// MemoryAction 表示一条记忆操作。
type MemoryAction struct {
	Action      string   `json:"action"` // create | update | delete
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Importance  int      `json:"importance"`
	Tags        []string `json:"tags"`
	Content     string   `json:"content"`
}

// Record 表示单轮原始对话记录（L1 层）。
type Record struct {
	Round           int    `json:"round"`
	Timestamp       string `json:"timestamp"`
	UserInput       string `json:"user_input"`
	AssistantOutput string `json:"assistant_output"`
}
