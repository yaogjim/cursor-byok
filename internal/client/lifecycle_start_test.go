package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/internal/backend"
	"cursor/internal/cursor"
	"cursor/internal/mitm"
)

type fakeCursorApp struct {
	id               cursor.AppIdentity
	running          bool
	supportsQuit     bool
	quitKeepsRunning bool
	quitErr          error
	launchErr        error
	identifyErr      error
	quitCalls        int
	launchCalls      int
	steps            []string
}

func (f *fakeCursorApp) record(step string) {
	if f == nil {
		return
	}
	f.steps = append(f.steps, step)
}

func (f *fakeCursorApp) Identify() (cursor.AppIdentity, error) {
	if f.identifyErr != nil {
		return cursor.AppIdentity{}, f.identifyErr
	}
	if f.id.BundleID == "" && f.id.AppPath == "" {
		return cursor.AppIdentity{}, cursor.ErrAppNotFound
	}
	return f.id, nil
}

func (f *fakeCursorApp) Running(context.Context, cursor.AppIdentity) (bool, error) {
	return f.running, nil
}

func (f *fakeCursorApp) SupportsGracefulQuit() bool {
	return f.supportsQuit
}

func (f *fakeCursorApp) QuitGracefully(ctx context.Context, _ cursor.AppIdentity) error {
	f.quitCalls++
	f.record("quit")
	if f.quitErr != nil {
		return f.quitErr
	}
	if f.quitKeepsRunning {
		<-ctx.Done()
		return cursor.ErrGracefulQuitTimeout
	}
	f.running = false
	return nil
}

func (f *fakeCursorApp) Launch(context.Context, cursor.AppIdentity) error {
	f.launchCalls++
	f.record("launch")
	if f.launchErr != nil {
		return f.launchErr
	}
	f.running = true
	return nil
}

func newCursorStartTestService(t *testing.T, startBackend bool) (*ProxyService, *fakeCursorApp) {
	t.Helper()
	service := newLifecycleTestService(t)
	if !startBackend && service.backendHost != nil && service.backendHost.IsRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.backendHost.Stop(ctx); err != nil {
			t.Fatalf("stop backend: %v", err)
		}
	}
	fake := &fakeCursorApp{
		id:           cursor.AppIdentity{BundleID: "com.example.fake-cursor", AppPath: filepath.Join(t.TempDir(), "FakeCursor.app")},
		supportsQuit: true,
	}
	configureCursorStartTestService(t, service, fake)
	return service, fake
}

func configureCursorStartTestService(t *testing.T, service *ProxyService, fake *fakeCursorApp) {
	t.Helper()
	service.cursorAppController = fake
	service.cursorQuitTimeout = 80 * time.Millisecond
	service.injectCursorUserInfoFn = func(string, string) error { return nil }
	service.clearSystemNodeExtraCACertsFn = func() error { return nil }
	service.applyCursorSettingsFn = func() error {
		if service.proxy == nil {
			return errors.New("proxy is not initialized")
		}
		if err := service.cursorSettingsStore.Apply(
			cursor.ProxyURLFromListenAddr(service.proxy.Snapshot().ListenAddr),
			service.cursorSettingsOwnerID,
		); err != nil {
			return err
		}
		service.setCursorSettingsApplied(true)
		return nil
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if service.proxy != nil {
			_ = service.proxy.Stop(ctx)
		}
	})
}

func rebuildCursorStartTestService(t *testing.T, previous *ProxyService, fake *fakeCursorApp) *ProxyService {
	t.Helper()
	settingsPath := filepath.Join(filepath.Dir(previous.configPath), "Cursor", "User", "settings.json")
	host, err := backend.NewHost(previous.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = host.Stop(ctx)
		_ = host.CloseObservability()
	})
	service := &ProxyService{
		backendHost:           host,
		store:                 previous.store,
		configPath:            previous.configPath,
		cursorSettingsStore:   cursor.NewUserProxySettingsStore(settingsPath),
		cursorSettingsOwnerID: previous.cursorSettingsOwnerID + "-restarted",
	}
	configureCursorStartTestService(t, service, fake)
	return service
}

func desiredProxyURL(t *testing.T, service *ProxyService) string {
	t.Helper()
	cfg, err := service.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cursor.ProxyURLFromListenAddr(cfg.ProxyListenAddr)
}

func readSettingsMap(t *testing.T, service *ProxyService) map[string]any {
	t.Helper()
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}
		}
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings=%s err=%v", data, err)
	}
	return settings
}

func TestInspectCursorProxyStartDoesNotStartOrWrite(t *testing.T) {
	service, fake := newCursorStartTestService(t, false)
	fake.running = true
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"editor.fontSize\": 12\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	ins, err := service.InspectCursorProxyStart()
	if err != nil {
		t.Fatal(err)
	}
	if !ins.NeedsRestart || !ins.CursorRunning || !ins.SettingsNeedChange {
		t.Fatalf("inspection = %+v", ins)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("inspect must not start backend")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("inspect wrote settings: %s", got)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("inspect quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestStartProxySettingsUnchangedDoesNotRestart(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	if err := service.cursorSettingsStore.Apply(desiredProxyURL(t, service), "pre-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartProxy(); err != nil {
		t.Fatal(err)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("unchanged restart quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestStartProxyAfterAppShutdownDoesNotRequireRepeatConfirmation(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	if _, err := service.StartProxyAfterRestartConfirm(); err != nil {
		t.Fatal(err)
	}
	wantURL := desiredProxyURL(t, service)
	if readSettingsMap(t, service)["http.proxy"] != wantURL {
		t.Fatalf("pre-shutdown http.proxy = %#v", readSettingsMap(t, service)["http.proxy"])
	}
	fake.quitCalls = 0
	fake.launchCalls = 0
	service.ShutdownForQuit()

	restarted := rebuildCursorStartTestService(t, service, fake)
	fake.running = true
	ins, err := restarted.InspectCursorProxyStart()
	if err != nil {
		t.Fatal(err)
	}
	if ins.NeedsRestart || ins.SettingsNeedChange || ins.ManualAction {
		t.Fatalf("unchanged app restart inspection = %+v", ins)
	}
	state, err := restarted.StartProxy()
	if err != nil {
		t.Fatal(err)
	}
	if !state.BackendRunning || !state.ProxyRunning || !state.CursorSettingsApplied {
		t.Fatalf("restarted services not ready: %+v", state)
	}
	plan, err := restarted.cursorSettingsStore.Plan(wantURL)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Owner.Present || plan.Owner.Value != restarted.cursorSettingsOwnerID {
		t.Fatalf("new instance did not take ownership: %+v", plan.Owner)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("unchanged app restart quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
	if readSettingsMap(t, restarted)["http.proxy"] != wantURL {
		t.Fatalf("http.proxy = %#v", readSettingsMap(t, restarted)["http.proxy"])
	}
	if _, err := restarted.StopProxy(); err != nil {
		t.Fatal(err)
	}
	if _, exists := readSettingsMap(t, restarted)["http.proxy"]; exists {
		t.Fatal("new owner could not disconnect after restart")
	}
}

func TestStartProxyAfterStopProxyStillRequiresConfirmationWhenRunning(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	if _, err := service.StartProxyAfterRestartConfirm(); err != nil {
		t.Fatal(err)
	}
	fake.quitCalls = 0
	fake.launchCalls = 0
	if _, err := service.StopProxy(); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSettingsMap(t, service)["http.proxy"]; ok {
		t.Fatal("StopProxy must clear injected proxy settings")
	}
	fake.running = true
	_, err := service.StartProxy()
	if !errors.Is(err, ErrCursorRestartConfirmationRequired) {
		t.Fatalf("err = %v", err)
	}
	if fake.quitCalls != 0 {
		t.Fatal("unconfirmed must not quit")
	}
}

func TestStartProxyAfterAppShutdownRequiresConfirmationWhenProxyURLChanges(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	if _, err := service.StartProxyAfterRestartConfirm(); err != nil {
		t.Fatal(err)
	}
	service.ShutdownForQuit()

	// Change the persisted config before constructing the new host, just as an
	// application restart loads its config before publishing the host snapshot.
	cfg, err := service.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyListenAddr = mustFreeListenAddr(t)
	if _, err := service.store.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	restarted := rebuildCursorStartTestService(t, service, fake)
	fake.running = true
	fake.quitCalls = 0
	_, err = restarted.StartProxy()
	if !errors.Is(err, ErrCursorRestartConfirmationRequired) {
		t.Fatalf("err = %v", err)
	}
	if fake.quitCalls != 0 {
		t.Fatal("unconfirmed must not quit")
	}
}

func TestStartProxyCursorNotRunningDoesNotLaunch(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = false
	if _, err := service.StartProxy(); err != nil {
		t.Fatal(err)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("not running quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
	if readSettingsMap(t, service)["http.proxy"] != desiredProxyURL(t, service) {
		t.Fatalf("settings not applied: %#v", readSettingsMap(t, service)["http.proxy"])
	}
}

func TestStartProxyRequiresConfirmationWhenRunningNeedsChange(t *testing.T) {
	service, fake := newCursorStartTestService(t, false)
	fake.running = true
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.StartProxy()
	if !errors.Is(err, ErrCursorRestartConfirmationRequired) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(service.GetState().LastError, cursorRestartConfirmationCode) {
		t.Fatalf("lastError = %q", service.GetState().LastError)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("unconfirmed start must keep old service state")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("unconfirmed wrote settings: %s", got)
	}
	if fake.quitCalls != 0 {
		t.Fatal("unconfirmed must not quit")
	}
}

func TestStartProxyUnsupportedQuitIsManualAction(t *testing.T) {
	service, fake := newCursorStartTestService(t, false)
	fake.running = true
	fake.supportsQuit = false
	_, err := service.StartProxy()
	if !errors.Is(err, ErrCursorQuitUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if fake.quitCalls != 0 {
		t.Fatal("unsupported platform must not quit or kill")
	}
	if service.backendHost.IsRunning() {
		t.Fatal("manual-action outcome must keep old service state")
	}
}

func TestStartProxyConfirmedRestartsAndApplies(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	if _, err := service.StartProxyAfterRestartConfirm(); err != nil {
		t.Fatal(err)
	}
	if fake.quitCalls != 1 || fake.launchCalls != 1 {
		t.Fatalf("quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
	if readSettingsMap(t, service)["http.proxy"] != desiredProxyURL(t, service) {
		t.Fatalf("http.proxy = %#v", readSettingsMap(t, service)["http.proxy"])
	}
	if !service.GetState().BackendRunning || !service.GetState().ProxyRunning {
		t.Fatalf("state = %+v", service.GetState())
	}
}

func TestStartProxyQuitTimeoutKeepsOldState(t *testing.T) {
	service, fake := newCursorStartTestService(t, false)
	fake.running = true
	fake.quitKeepsRunning = true
	injectCalls := 0
	service.injectCursorUserInfoFn = func(string, string) error {
		injectCalls++
		fake.record("inject")
		return nil
	}
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.StartProxyAfterRestartConfirm()
	if !errors.Is(err, ErrCursorQuitTimeout) {
		t.Fatalf("err = %v", err)
	}
	if injectCalls != 0 {
		t.Fatalf("timeout must not inject account, got %d", injectCalls)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("timeout must restore service pre-state")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("timeout wrote settings: %s", got)
	}
	if fake.launchCalls != 0 {
		t.Fatal("timeout must not launch")
	}
}

func TestStartProxySettingsFailureDoesNotStopExistingServices(t *testing.T) {
	service, _ := newCursorStartTestService(t, true)
	cfg, err := service.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := mitm.NewProxyServer(cfg.ProxyListenAddr, "http://"+cfg.BackendListenAddr, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	service.proxy = proxy
	service.applyCursorSettingsFn = func() error {
		return errors.New("injected settings failure")
	}
	if !service.backendHost.IsRunning() || !service.proxy.IsRunning() {
		t.Fatal("pre-state services must be running")
	}
	_, err = service.StartProxy()
	if err == nil {
		t.Fatal("expected settings failure")
	}
	if !service.backendHost.IsRunning() {
		t.Fatal("settings failure stopped existing backend")
	}
	if !service.proxy.IsRunning() {
		t.Fatal("settings failure stopped existing mitm")
	}
}

func TestStartProxyFailedApplyPreservesAnotherInstancesSettings(t *testing.T) {
	service, _ := newCursorStartTestService(t, false)
	service.applyCursorSettingsFn = func() error {
		// A second instance takes ownership after StartProxy's snapshot.
		if err := service.cursorSettingsStore.Apply("http://127.0.0.1:19995", "other-instance"); err != nil {
			t.Fatal(err)
		}
		return errors.New("injected failure before this instance writes settings")
	}
	_, err := service.StartProxy()
	if err == nil || !strings.Contains(err.Error(), "未能恢复原设置") {
		t.Fatalf("ownership conflict must remain visible: %v", err)
	}
	if readSettingsMap(t, service)["http.proxy"] != "http://127.0.0.1:19995" {
		t.Fatal("failed start overwrote another instance's settings")
	}
	snap, err := service.cursorSettingsStore.Plan("http://127.0.0.1:19995")
	if err != nil || snap.Owner.Value != "other-instance" {
		t.Fatalf("failed start changed settings ownership: %+v, err=%v", snap.Owner, err)
	}
	if service.backendHost.IsRunning() || (service.proxy != nil && service.proxy.IsRunning()) {
		t.Fatal("failed start did not clean up its own new services")
	}
}

func TestStartProxySettingsFailureCleansOnlyNewlyStarted(t *testing.T) {
	service, _ := newCursorStartTestService(t, false)
	service.applyCursorSettingsFn = func() error {
		if err := service.cursorSettingsStore.Apply(
			cursor.ProxyURLFromListenAddr(service.proxy.Snapshot().ListenAddr),
			service.cursorSettingsOwnerID,
		); err != nil {
			return err
		}
		return errors.New("injected settings failure")
	}
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\",\n  \"editor.fontSize\": 13\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.StartProxy()
	if err == nil {
		t.Fatal("expected settings failure")
	}
	if service.backendHost.IsRunning() {
		t.Fatal("newly started backend must be cleaned")
	}
	if service.proxy != nil && service.proxy.IsRunning() {
		t.Fatal("newly started mitm must be cleaned")
	}
	settings := readSettingsMap(t, service)
	if settings["http.proxy"] != "http://127.0.0.1:19991" {
		t.Fatalf("http.proxy rolled back to %#v", settings["http.proxy"])
	}
	if settings["editor.fontSize"] != float64(13) {
		t.Fatalf("unrelated key = %#v", settings["editor.fontSize"])
	}
}

func TestStartProxySettingsFailureAfterQuitRelaunchesOnce(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	service.applyCursorSettingsFn = func() error {
		_ = service.cursorSettingsStore.Apply(
			cursor.ProxyURLFromListenAddr(service.proxy.Snapshot().ListenAddr),
			service.cursorSettingsOwnerID,
		)
		return errors.New("injected settings failure")
	}
	_, err := service.StartProxyAfterRestartConfirm()
	if err == nil {
		t.Fatal("expected settings failure")
	}
	if !strings.Contains(err.Error(), "已恢复原设置") {
		t.Fatalf("successful restore must be visible: %v", err)
	}
	if fake.quitCalls != 1 || fake.launchCalls != 1 {
		t.Fatalf("quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestStartProxyLaunchFailureAfterSettingsIsPartialSuccess(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	fake.launchErr = errors.New("open failed")
	var emitted ProxyState
	service.emitProxyStateFn = func(state ProxyState) { emitted = state }
	_, err := service.StartProxyAfterRestartConfirm()
	if !errors.Is(err, ErrCursorLaunchPartial) {
		t.Fatalf("err = %v", err)
	}
	if !emitted.Running || !strings.Contains(emitted.LastError, cursorLaunchPartialCode) {
		t.Fatalf("partial failure was lost from state event: %+v", emitted)
	}
	service.ClearLastError()
	if emitted.LastError != "" {
		t.Fatalf("explicit clear did not clear event error: %q", emitted.LastError)
	}
	if !service.backendHost.IsRunning() || service.proxy == nil || !service.proxy.IsRunning() {
		t.Fatal("partial success must keep new services")
	}
	if readSettingsMap(t, service)["http.proxy"] != desiredProxyURL(t, service) {
		t.Fatal("partial success must keep new settings")
	}
	if fake.launchCalls != 1 {
		t.Fatalf("must not retry launch, got %d", fake.launchCalls)
	}
}

func TestStartProxyUnsupportedUnknownDoesNotWrite(t *testing.T) {
	service, fake := newCursorStartTestService(t, false)
	fake.supportsQuit = false
	fake.identifyErr = cursor.ErrUnsupportedPlatform
	fake.running = false
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	ins, err := service.InspectCursorProxyStart()
	if err != nil {
		t.Fatal(err)
	}
	if !ins.ManualAction || !ins.SettingsNeedChange || ins.NeedsRestart {
		t.Fatalf("inspection = %+v", ins)
	}
	if ins.CursorRunning {
		t.Fatal("unsupported/unknown must not claim Cursor is running")
	}
	if strings.Contains(ins.Message, "未运行") {
		t.Fatalf("must not claim not running: %q", ins.Message)
	}
	_, err = service.StartProxy()
	if !errors.Is(err, ErrCursorQuitUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("unsupported start must keep old service state")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("unsupported wrote settings: %s", got)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestStartProxyUnsupportedUnchangedStillStarts(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.supportsQuit = false
	fake.identifyErr = cursor.ErrUnsupportedPlatform
	if err := service.cursorSettingsStore.Apply(desiredProxyURL(t, service), "pre-owner"); err != nil {
		t.Fatal(err)
	}
	ins, err := service.InspectCursorProxyStart()
	if err != nil {
		t.Fatal(err)
	}
	if ins.ManualAction || ins.NeedsRestart || ins.SettingsNeedChange {
		t.Fatalf("unchanged unsupported inspection = %+v", ins)
	}
	if _, err := service.StartProxy(); err != nil {
		t.Fatal(err)
	}
	if fake.quitCalls != 0 || fake.launchCalls != 0 {
		t.Fatalf("unchanged unsupported quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestInspectCursorProxyStartConcurrentReturnsBusy(t *testing.T) {
	service, _ := newCursorStartTestService(t, false)
	service.lifecycleMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := service.InspectCursorProxyStart()
		done <- err
	}()
	select {
	case err := <-done:
		service.lifecycleMu.Unlock()
		if !errors.Is(err, ErrCursorStartBusy) {
			t.Fatalf("inspection should report busy, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		service.lifecycleMu.Unlock()
		<-done
		t.Fatal("inspection queued behind an active lifecycle operation")
	}
	if service.backendHost.IsRunning() {
		t.Fatal("busy inspection must not start backend")
	}
}

func TestStartProxyConcurrentReturnsBusy(t *testing.T) {
	service, _ := newCursorStartTestService(t, false)
	locked := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		service.lifecycleMu.Lock()
		close(locked)
		<-release
		service.lifecycleMu.Unlock()
	}()
	<-locked
	started := time.Now()
	_, err := service.StartProxy()
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("busy path waited %s", elapsed)
	}
	if !errors.Is(err, ErrCursorStartBusy) {
		t.Fatalf("err = %v", err)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("busy start must not start backend")
	}
}

func TestStartProxyRestoreFailureIsVisible(t *testing.T) {
	service, _ := newCursorStartTestService(t, false)
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	service.applyCursorSettingsFn = func() error {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(path) })
		return errors.New("injected settings failure")
	}
	_, err := service.StartProxy()
	if err == nil {
		t.Fatal("expected settings failure")
	}
	if !strings.Contains(err.Error(), "未能恢复原设置") {
		t.Fatalf("restore failure must be visible: %v", err)
	}
	if strings.Contains(err.Error(), "已恢复原设置") {
		t.Fatalf("must not claim restore success: %v", err)
	}
	if service.backendHost.IsRunning() {
		t.Fatal("newly started backend must be cleaned")
	}
}

func TestStartProxyRelaunchFailureAfterSettingsRollbackIsVisible(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	fake.launchErr = errors.New("open failed")
	service.applyCursorSettingsFn = func() error {
		_ = service.cursorSettingsStore.Apply(
			cursor.ProxyURLFromListenAddr(service.proxy.Snapshot().ListenAddr),
			service.cursorSettingsOwnerID,
		)
		return errors.New("injected settings failure")
	}
	_, err := service.StartProxyAfterRestartConfirm()
	if err == nil {
		t.Fatal("expected settings failure")
	}
	if errors.Is(err, ErrCursorLaunchPartial) {
		t.Fatal("settings rollback with relaunch failure is not partial new-settings success")
	}
	if !strings.Contains(err.Error(), "已恢复原设置") {
		t.Fatalf("restore success must be visible: %v", err)
	}
	if !strings.Contains(err.Error(), "请手动启动") {
		t.Fatalf("relaunch failure must be visible: %v", err)
	}
	if fake.quitCalls != 1 || fake.launchCalls != 1 {
		t.Fatalf("quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
}

func TestStartProxyConfirmedRestartOrderIsQuitInjectSettingsLaunch(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	service.injectCursorUserInfoFn = func(string, string) error {
		fake.record("inject")
		return nil
	}
	origApply := service.applyCursorSettingsFn
	service.applyCursorSettingsFn = func() error {
		fake.record("settings")
		return origApply()
	}
	if _, err := service.StartProxyAfterRestartConfirm(); err != nil {
		t.Fatal(err)
	}
	want := []string{"quit", "inject", "settings", "launch"}
	if strings.Join(fake.steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", fake.steps, want)
	}
}

func TestStartProxyInjectFailureAfterQuitCompensates(t *testing.T) {
	service, fake := newCursorStartTestService(t, true)
	fake.running = true
	path := filepath.Join(filepath.Dir(service.configPath), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"http.proxy\": \"http://127.0.0.1:19991\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	service.injectCursorUserInfoFn = func(string, string) error {
		fake.record("inject")
		return errors.New("injected account failure")
	}
	origApply := service.applyCursorSettingsFn
	service.applyCursorSettingsFn = func() error {
		fake.record("settings")
		return origApply()
	}
	_, err := service.StartProxyAfterRestartConfirm()
	if err == nil {
		t.Fatal("expected inject failure")
	}
	if !strings.Contains(err.Error(), "已保持原设置") {
		t.Fatalf("inject failure must keep old settings: %v", err)
	}
	if strings.Contains(err.Error(), "已恢复原设置") {
		t.Fatalf("must not claim settings restore: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("inject failure wrote settings: %s", got)
	}
	if fake.quitCalls != 1 || fake.launchCalls != 1 {
		t.Fatalf("quit=%d launch=%d", fake.quitCalls, fake.launchCalls)
	}
	for _, step := range fake.steps {
		if step == "settings" {
			t.Fatalf("inject failure must not apply settings: %v", fake.steps)
		}
	}
}
