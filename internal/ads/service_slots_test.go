package ads

import (
	"context"
	"strings"
	"testing"
)

// 内容服务未接入前，默认不得配置任何远端广告位，避免继续向旧地址拉取。
func TestNoRemoteSlotsConfiguredByDefault(t *testing.T) {
	if len(Slots) != 0 {
		t.Fatalf("Slots = %+v, want no remote slots", Slots)
	}
	for _, slot := range Slots {
		if strings.Contains(slot.FetchURL, "leokun.cn") {
			t.Fatalf("slot %q still points to legacy host", slot.FetchURL)
		}
	}
}

func TestRefreshWithoutSlotsDoesNotFetch(t *testing.T) {
	service := NewService(Options{StoreRoot: t.TempDir(), Enabled: true})
	runtimeState, changed, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if changed {
		t.Fatalf("Refresh() changed = true, want false")
	}
	if len(runtimeState.Slots) != 0 || runtimeState.Available {
		t.Fatalf("runtime = %+v, want empty slots", runtimeState)
	}
}

func TestLoadAssetIgnoresUnconfiguredSlot(t *testing.T) {
	service := NewService(Options{StoreRoot: t.TempDir(), Enabled: true})
	if _, _, ok, err := service.LoadAsset(context.Background(), "/ad/index.html"); err != nil || ok {
		t.Fatalf("LoadAsset() ok=%v err=%v, want no asset without configured slot", ok, err)
	}
}
