// provider_image_budget.go 负责按请求体字节预算裁剪自动补入的历史图片。
package forwarder

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
)

const (
	// providerRequestDefaultConnectTimeoutSeconds 与配置层默认建连超时保持一致。
	providerRequestDefaultConnectTimeoutSeconds = int64(30)
	// providerRequestUploadBytesPerSecond 是预算推导使用的保守上行带宽假设。
	// 没有实测依据；拿到真实 wrote_request_ms 后应重新校准。
	providerRequestUploadBytesPerSecond = int64(256 * 1024)
	// providerRequestMinBudgetBytes 防止极短建连超时把预算压到无法容纳任何图片。
	providerRequestMinBudgetBytes = int64(2 << 20)
	// providerRequestMaxBudgetBytes 与传输层 32 MiB 硬上限保持距离。
	providerRequestMaxBudgetBytes = int64(16 << 20)
	// providerRequestEstimateTextFactorNumerator/Denominator 为 JSON 转义留 5% 冗余。
	providerRequestEstimateTextFactorNumerator   = int64(21)
	providerRequestEstimateTextFactorDenominator = int64(20)
)

// providerImageRetention 记录本次请求的图片去重与预算裁剪结果。
type providerImageRetention struct {
	Deduped         int
	Omitted         int
	UserOmitted     int
	OverBudgetBytes int64
	OmittedBytes    int64
	RetainedBytes   int64
	BudgetBytes     int64
	EstimatedBytes  int64
}

// Summary 生成不含路径与图片内容的诊断摘要，随请求工件落盘。
func (retention providerImageRetention) Summary() string {
	return fmt.Sprintf(
		"deduped=%d;omitted=%d;omitted_bytes=%d;retained_image_bytes=%d;budget_bytes=%d;estimated_bytes=%d;user_omitted=%d;over_budget=%t;over_budget_bytes=%d",
		retention.Deduped,
		retention.Omitted,
		retention.OmittedBytes,
		retention.RetainedBytes,
		retention.BudgetBytes,
		retention.EstimatedBytes,
		retention.UserOmitted,
		retention.OverBudgetBytes > 0,
		retention.OverBudgetBytes,
	)
}

// providerRequestByteBudget 由建连超时推导请求体预算：上传必须能在建连窗口内完成。
func providerRequestByteBudget(connectTimeoutSeconds int64) int64 {
	if connectTimeoutSeconds <= 0 {
		connectTimeoutSeconds = providerRequestDefaultConnectTimeoutSeconds
	}
	budget := connectTimeoutSeconds * providerRequestUploadBytesPerSecond
	if budget < providerRequestMinBudgetBytes {
		return providerRequestMinBudgetBytes
	}
	if budget > providerRequestMaxBudgetBytes {
		return providerRequestMaxBudgetBytes
	}
	return budget
}

// applyProviderImageBudget 先去重工具图，再依次裁剪旧工具图与历史轮用户图。
// 当前轮及来源轮次未知的用户图始终保留，canonical history 不会被修改。
func applyProviderImageBudget(messages []modeladapter.Message, tools []json.RawMessage, budgetBytes int64, currentTurnSeq int64) ([]modeladapter.Message, providerImageRetention) {
	retention := providerImageRetention{BudgetBytes: budgetBytes}
	cloned := cloneProviderEnrichmentMessages(messages)
	firstByImage := make(map[[sha256.Size]byte]string)
	candidates := make([]int, 0, len(cloned))
	for index := range cloned {
		message := &cloned[index]
		if !hasProviderToolImage(message.ContentParts) {
			continue
		}
		if strings.TrimSpace(message.Role) != "tool" {
			retention.RetainedBytes += providerToolImageBytes(message.ContentParts)
			continue
		}
		key, ok := providerToolImageKey(message.ContentParts)
		if !ok {
			continue
		}
		if firstCallID, seen := firstByImage[key]; seen {
			replaceProviderToolImageWithText(message, duplicateProviderImagePlaceholder(*message, firstCallID))
			retention.Deduped++
			continue
		}
		firstByImage[key] = strings.TrimSpace(message.ToolCallID)
		candidates = append(candidates, index)
		retention.RetainedBytes += providerToolImageBytes(message.ContentParts)
	}
	estimated := estimateProviderRequestBytes(cloned, tools)
	if budgetBytes > 0 {
		for _, index := range candidates {
			if estimated <= budgetBytes {
				break
			}
			message := &cloned[index]
			imageBytes := providerToolImageBytes(message.ContentParts)
			replaceProviderToolImageWithText(message, omittedProviderImagePlaceholder(*message))
			retention.Omitted++
			retention.OmittedBytes += imageBytes
			retention.RetainedBytes -= imageBytes
			estimated = estimateProviderRequestBytes(cloned, tools)
		}
	}
	// 按部件逐张占位，避免同一条历史用户消息中的其余图片被连带删除。
	if budgetBytes > 0 {
		for index := range cloned {
			message := &cloned[index]
			if strings.TrimSpace(message.Role) != "user" || message.SourceTurnSeq <= 0 || message.SourceTurnSeq >= currentTurnSeq {
				continue
			}
			for partIndex, part := range message.ContentParts {
				if estimated <= budgetBytes {
					break
				}
				if !strings.EqualFold(strings.TrimSpace(part.Type), "image") || part.Image == nil {
					continue
				}
				imageBytes := providerDataURLBytes(part.Image)
				placeholder := fmt.Sprintf("[historical user image omitted to fit provider request size: %d bytes; ask the user to re-attach the image if needed]", len(part.Image.Data))
				message.ContentParts[partIndex] = modeladapter.ContentPart{Type: "text", Text: placeholder}
				retention.UserOmitted++
				retention.Omitted++
				retention.OmittedBytes += imageBytes
				retention.RetainedBytes -= imageBytes
				// 两种端点在最后一张图片被替换后都会回退到文本路径。
				if !hasProviderToolImage(message.ContentParts) {
					texts := make([]string, 0, len(message.ContentParts))
					for _, remaining := range message.ContentParts {
						if remaining.Type == "text" && remaining.Text != "" {
							texts = append(texts, remaining.Text)
						}
					}
					message.Content = strings.Join(texts, "\n")
				}
				estimated = estimateProviderRequestBytes(cloned, tools)
			}
		}
	}
	retention.EstimatedBytes = estimated
	if budgetBytes > 0 && estimated > budgetBytes {
		retention.OverBudgetBytes = estimated - budgetBytes
	}
	return cloned, retention
}

func hasProviderToolImage(parts []modeladapter.ContentPart) bool {
	for _, part := range parts {
		if strings.EqualFold(strings.TrimSpace(part.Type), "image") && part.Image != nil {
			return true
		}
	}
	return false
}

func providerToolImageKey(parts []modeladapter.ContentPart) ([sha256.Size]byte, bool) {
	hash := sha256.New()
	wrote := false
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "image") || part.Image == nil {
			continue
		}
		hash.Write([]byte(strings.ToLower(strings.TrimSpace(part.Image.MIMEType))))
		hash.Write([]byte{0})
		hash.Write(part.Image.Data)
		hash.Write([]byte{0})
		wrote = true
	}
	var key [sha256.Size]byte
	if !wrote {
		return key, false
	}
	copy(key[:], hash.Sum(nil))
	return key, true
}

func providerToolImageBytes(parts []modeladapter.ContentPart) int64 {
	total := int64(0)
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "image") || part.Image == nil {
			continue
		}
		total += providerDataURLBytes(part.Image)
	}
	return total
}

func providerDataURLBytes(image *modeladapter.ImageContent) int64 {
	if image == nil {
		return 0
	}
	mimeType := strings.TrimSpace(image.MIMEType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return int64(base64.StdEncoding.EncodedLen(len(image.Data)) + len("data:") + len(mimeType) + len(";base64,"))
}

func replaceProviderToolImageWithText(message *modeladapter.Message, text string) {
	if message == nil {
		return
	}
	message.Content = text
	message.ContentParts = nil
}

func omittedProviderImagePlaceholder(message modeladapter.Message) string {
	path, size := providerToolImageIdentity(message.ContentParts)
	name := path
	if name == "" {
		name = "image"
	}
	return fmt.Sprintf(
		"[image omitted to fit provider request size: %s, %d bytes; run Read on this file to view the image again]",
		name,
		size,
	)
}

func duplicateProviderImagePlaceholder(message modeladapter.Message, firstCallID string) string {
	path, _ := providerToolImageIdentity(message.ContentParts)
	name := path
	if name == "" {
		name = "this file"
	}
	if callID := strings.TrimSpace(firstCallID); callID != "" {
		return fmt.Sprintf(
			"[image omitted: duplicate of the image returned by tool call %s; run Read on %s to view it again]",
			callID,
			name,
		)
	}
	return fmt.Sprintf(
		"[image omitted: duplicate of an earlier image; run Read on %s to view it again]",
		name,
	)
}

func providerToolImageIdentity(parts []modeladapter.ContentPart) (string, int) {
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "image") || part.Image == nil {
			continue
		}
		return strings.TrimSpace(part.Image.Path), len(part.Image.Data)
	}
	return "", 0
}

// estimateProviderRequestBytes 估算最终编码请求体字节数：文本留 5% 冗余，
// 图片按 base64 精确长度计入（base64 字符不会触发 JSON 转义）。
func estimateProviderRequestBytes(messages []modeladapter.Message, tools []json.RawMessage) int64 {
	stripped := make([]modeladapter.Message, len(messages))
	imageBytes := int64(0)
	for index, message := range messages {
		stripped[index] = message
		if len(message.ContentParts) == 0 {
			continue
		}
		parts := make([]modeladapter.ContentPart, len(message.ContentParts))
		copy(parts, message.ContentParts)
		for partIndex := range parts {
			if parts[partIndex].Image == nil {
				continue
			}
			imageBytes += providerDataURLBytes(parts[partIndex].Image)
			imageCopy := *parts[partIndex].Image
			imageCopy.Data = nil
			parts[partIndex].Image = &imageCopy
		}
		stripped[index].ContentParts = parts
	}
	encoded, err := json.Marshal(stripped)
	base := int64(0)
	if err == nil {
		base = int64(len(encoded))
	}
	for _, tool := range tools {
		base += int64(len(tool))
	}
	return base*providerRequestEstimateTextFactorNumerator/providerRequestEstimateTextFactorDenominator + imageBytes
}
