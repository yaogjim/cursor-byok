package byok

import (
	"fmt"
	"os"
	"strings"

	"cursor-cli-model-pool/internal/identity"
	"cursor-cli-model-pool/internal/paths"

	"gopkg.in/yaml.v3"
)

type Physical struct {
	ID              string
	FallbackEnabled bool
}

type fileConfig struct {
	ModelAdapters []adapter `yaml:"modelAdapters"`
}

type adapter struct {
	DisplayName      string           `yaml:"displayName"`
	Type             string           `yaml:"type"`
	BaseURL          string           `yaml:"baseURL"`
	APIKey           string           `yaml:"apiKey"`
	ModelID          string           `yaml:"modelID"`
	OpenAIEndpoint   string           `yaml:"openAIEndpoint"`
	ProviderFallback providerFallback `yaml:"providerFallback"`
}

type providerFallback struct {
	Enabled bool `yaml:"enabled"`
}

func Load() ([]Physical, error) {
	path, err := paths.BYOKConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFile(path)
}

func LoadFile(path string) ([]Physical, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 BYOK 配置失败")
	}
	var file fileConfig
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("BYOK 配置不是合法 YAML")
	}
	out := make([]Physical, 0, len(file.ModelAdapters))
	seen := map[string]int{}
	for i := range file.ModelAdapters {
		item := file.ModelAdapters[i]
		if isEmpty(item) {
			zeroAdapter(&file.ModelAdapters[i])
			continue
		}
		id, err := identity.Compute(item.BaseURL, item.Type, item.ModelID, item.APIKey, item.DisplayName, item.OpenAIEndpoint)
		zeroAdapter(&file.ModelAdapters[i])
		if err != nil {
			return nil, fmt.Errorf("无法计算适配器身份")
		}
		seen[id]++
		out = append(out, Physical{ID: id, FallbackEnabled: item.ProviderFallback.Enabled})
	}
	for id, n := range seen {
		if n > 1 {
			_ = id
			return nil, fmt.Errorf("派生渠道 ID 重复")
		}
	}
	return out, nil
}

func isEmpty(item adapter) bool {
	return strings.TrimSpace(item.DisplayName) == "" &&
		strings.TrimSpace(item.Type) == "" &&
		strings.TrimSpace(item.BaseURL) == "" &&
		strings.TrimSpace(item.APIKey) == "" &&
		strings.TrimSpace(item.ModelID) == ""
}

func zeroAdapter(item *adapter) {
	item.APIKey = ""
	item.BaseURL = ""
	item.DisplayName = ""
	item.ModelID = ""
	item.OpenAIEndpoint = ""
	item.Type = ""
}
