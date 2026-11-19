package ads

const (
	RoutePrefix  = "/ad"
	EventUpdated = "ad:updated"
)

// Slot 描述一个可拉取的广告位。内容服务未接入前不配置任何远端地址；
// 接入自定义服务器时在此注入，接口契约见
// docs/features/ad-content-service/design.md。
type Slot struct {
	ID       string
	FetchURL string
}

var Slots []Slot

type WindowConfig struct {
	Width  int `json:"width" yaml:"width"`
	Height int `json:"height" yaml:"height"`
}

type HomePlacementConfig struct {
	Title    string `json:"title" yaml:"title"`
	Subtitle string `json:"subtitle" yaml:"subtitle"`
}

type Config struct {
	Enabled bool                `json:"enabled" yaml:"enabled"`
	Window  WindowConfig        `json:"window" yaml:"window"`
	Home    HomePlacementConfig `json:"home" yaml:"home"`
}

type Runtime struct {
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
	Slots        []SlotRuntime       `json:"slots"`
}

type SlotRuntime struct {
	ID           string              `json:"id"`
	Available    bool                `json:"available"`
	Enabled      bool                `json:"enabled"`
	PackageHash  string              `json:"packageHash"`
	AssetBaseURL string              `json:"assetBaseURL"`
	IndexURL     string              `json:"indexURL"`
	Window       WindowConfig        `json:"window"`
	Home         HomePlacementConfig `json:"home"`
}

type MetricsSnapshot struct {
	TurnsTotal         int
	RequestTokensTotal int64
	PromptTokensTotal  int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
}

type FetchResult struct {
	Hash    string
	Changed bool
}
