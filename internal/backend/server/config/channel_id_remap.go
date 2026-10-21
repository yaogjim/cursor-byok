package config

import "strings"

// deriveChannelIDRemap maps submitted previous channel IDs onto the newly
// computed IDs of the same adapters after identity fields change. Existing
// adapters carry their last persisted ID as an identity hint; newly added
// adapters have no hint, so delete+add replacements cannot be paired by guess.
func deriveChannelIDRemap(previous, submitted, next []ModelAdapterConfig) map[string]string {
	if len(previous) == 0 || len(submitted) != len(next) {
		return nil
	}
	previousIDs := adapterIDSet(previous)
	seenHints := make(map[string]struct{}, len(submitted))
	remap := make(map[string]string)
	for index := range submitted {
		oldID := strings.TrimSpace(submitted[index].ID)
		newID := strings.TrimSpace(next[index].ID)
		if oldID == "" || newID == "" {
			continue
		}
		if _, exists := previousIDs[oldID]; !exists {
			continue
		}
		if _, duplicate := seenHints[oldID]; duplicate {
			return nil
		}
		seenHints[oldID] = struct{}{}
		if oldID != newID {
			remap[oldID] = newID
		}
	}
	if len(remap) == 0 {
		return nil
	}
	return remap
}

func applyChannelIDRemap(adapters []ModelAdapterConfig, remap map[string]string) {
	if len(remap) == 0 {
		return
	}
	for index := range adapters {
		fb := adapters[index].ProviderFallback
		fb.PrimaryChannelID = rewriteChannelID(fb.PrimaryChannelID, remap)
		if len(fb.CandidateChannelIDs) > 0 {
			copied := append([]string(nil), fb.CandidateChannelIDs...)
			for candidateIndex, candidateID := range copied {
				copied[candidateIndex] = rewriteChannelID(candidateID, remap)
			}
			fb.CandidateChannelIDs = copied
		}
		adapters[index].ProviderFallback = fb
	}
}

func applyChannelIDRemapToGateway(gateway *GatewayConfig, remap map[string]string) {
	if gateway == nil || len(remap) == 0 || len(gateway.PublicModels) == 0 {
		return
	}
	models := append([]GatewayPublicModel(nil), gateway.PublicModels...)
	for index := range models {
		models[index].TargetAdapterID = rewriteChannelID(models[index].TargetAdapterID, remap)
	}
	gateway.PublicModels = models
}

func rewriteChannelID(value string, remap map[string]string) string {
	id := strings.TrimSpace(value)
	if id == "" || len(remap) == 0 {
		return id
	}
	if mapped, ok := remap[id]; ok && mapped != "" {
		return mapped
	}
	return id
}

func adapterIDSet(adapters []ModelAdapterConfig) map[string]struct{} {
	ids := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		id := strings.TrimSpace(adapter.ID)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}
