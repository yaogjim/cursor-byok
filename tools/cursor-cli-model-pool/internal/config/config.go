package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cursor-cli-model-pool/internal/paths"

	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion         = 1
	ModeAsk               = "ask"
	ModePlan              = "plan"
	ModeWrite             = "write"
	DefaultWorktreePrefix = "cursor-pool"
)

var worktreePrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
var modelIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{16}$`)

type Safety struct {
	AllowWrite bool
}

type Config struct {
	SchemaVersion      int
	AgentPath          string
	Endpoint           string
	Models             []string
	Mode               string
	WorktreeNamePrefix string
	Safety             Safety
}

func Load() (Config, error) {
	path, err := paths.PoolConfigPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取 CLI 模型池配置失败")
	}
	return Parse(data)
}

func Parse(data []byte) (Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, fmt.Errorf("CLI 模型池配置不是合法 YAML")
	}
	root, err := mappingRoot(&doc)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		WorktreeNamePrefix: DefaultWorktreePrefix,
	}
	seen := map[string]struct{}{}
	for i := 0; i < len(root.Content); i += 2 {
		key := strings.TrimSpace(root.Content[i].Value)
		value := root.Content[i+1]
		if err := rejectForbiddenKey(key); err != nil {
			return Config{}, err
		}
		if _, dup := seen[key]; dup {
			return Config{}, fmt.Errorf("重复字段 %s", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "schemaVersion":
			if err := value.Decode(&cfg.SchemaVersion); err != nil {
				return Config{}, fmt.Errorf("schemaVersion 必须为整数 1")
			}
		case "agentPath":
			if err := value.Decode(&cfg.AgentPath); err != nil {
				return Config{}, fmt.Errorf("agentPath 必须为字符串")
			}
			cfg.AgentPath = strings.TrimSpace(cfg.AgentPath)
		case "endpoint":
			if err := value.Decode(&cfg.Endpoint); err != nil {
				return Config{}, fmt.Errorf("endpoint 必须为字符串")
			}
			cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
		case "models":
			if err := value.Decode(&cfg.Models); err != nil {
				return Config{}, fmt.Errorf("models 必须为字符串列表")
			}
		case "mode":
			if err := value.Decode(&cfg.Mode); err != nil {
				return Config{}, fmt.Errorf("mode 必须为字符串")
			}
			cfg.Mode = strings.TrimSpace(cfg.Mode)
		case "worktreeNamePrefix":
			if err := value.Decode(&cfg.WorktreeNamePrefix); err != nil {
				return Config{}, fmt.Errorf("worktreeNamePrefix 必须为字符串")
			}
			cfg.WorktreeNamePrefix = strings.TrimSpace(cfg.WorktreeNamePrefix)
			if cfg.WorktreeNamePrefix == "" {
				cfg.WorktreeNamePrefix = DefaultWorktreePrefix
			}
		case "safety":
			safety, err := parseSafety(value)
			if err != nil {
				return Config{}, err
			}
			cfg.Safety = safety
		default:
			return Config{}, fmt.Errorf("未知字段 %s", key)
		}
	}
	return validate(cfg)
}

func parseSafety(node *yaml.Node) (Safety, error) {
	if node.Kind != yaml.MappingNode {
		return Safety{}, fmt.Errorf("safety 必须为对象")
	}
	var safety Safety
	for i := 0; i < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		value := node.Content[i+1]
		if err := rejectForbiddenKey(key); err != nil {
			return Safety{}, err
		}
		switch key {
		case "allowWrite":
			if err := value.Decode(&safety.AllowWrite); err != nil {
				return Safety{}, fmt.Errorf("safety.allowWrite 必须为布尔值")
			}
		default:
			return Safety{}, fmt.Errorf("未知字段 safety.%s", key)
		}
	}
	return safety, nil
}

func validate(cfg Config) (Config, error) {
	if cfg.SchemaVersion != SchemaVersion {
		return Config{}, fmt.Errorf("schemaVersion 必须为 %d", SchemaVersion)
	}
	if cfg.AgentPath == "" {
		return Config{}, fmt.Errorf("agentPath 不能为空")
	}
	if !filepath.IsAbs(cfg.AgentPath) {
		return Config{}, fmt.Errorf("agentPath 必须为绝对路径")
	}
	if cfg.Endpoint != paths.AllowedEndpoint {
		return Config{}, fmt.Errorf("endpoint 只允许 %s", paths.AllowedEndpoint)
	}
	switch cfg.Mode {
	case ModeAsk, ModePlan, ModeWrite:
	default:
		return Config{}, fmt.Errorf("mode 只允许 ask、plan 或 write")
	}
	if cfg.Mode == ModeWrite && !cfg.Safety.AllowWrite {
		return Config{}, fmt.Errorf("mode=write 时 safety.allowWrite 必须为 true")
	}
	if !worktreePrefixPattern.MatchString(cfg.WorktreeNamePrefix) {
		return Config{}, fmt.Errorf("worktreeNamePrefix 不符合字符白名单")
	}
	if len(cfg.Models) == 0 {
		return Config{}, fmt.Errorf("models 不能为空")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(cfg.Models))
	for _, raw := range cfg.Models {
		id := strings.TrimSpace(raw)
		if !modelIDPattern.MatchString(id) {
			return Config{}, fmt.Errorf("models 必须是精确 16 位十六进制物理模型 ID")
		}
		id = strings.ToLower(id)
		if _, ok := seen[id]; ok {
			return Config{}, fmt.Errorf("models 不能包含重复模型 ID")
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	cfg.Models = normalized
	return cfg, nil
}

func mappingRoot(doc *yaml.Node) (*yaml.Node, error) {
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("CLI 模型池配置必须是单个对象")
		}
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("CLI 模型池配置必须是对象")
	}
	return node, nil
}

func rejectForbiddenKey(key string) error {
	folded := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch folded {
	case "force", "yolo", "printenv":
		return fmt.Errorf("禁止字段 %s", key)
	}
	if isCredentialKey(folded) {
		return fmt.Errorf("禁止凭据字段")
	}
	return nil
}

func isCredentialKey(folded string) bool {
	switch {
	case folded == "apikey", folded == "token", folded == "credential", folded == "credentials":
		return true
	case folded == "password", folded == "secret", folded == "authorization":
		return true
	case folded == "cursortoken", folded == "accesstoken", folded == "refreshtoken":
		return true
	case strings.Contains(folded, "apikey"), strings.Contains(folded, "credential"):
		return true
	default:
		return false
	}
}
