package analyze

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"cursor-log-analyzer/internal/sanitize"
	"cursor-log-analyzer/internal/workspace"
)

const (
	mitmSampleLimit        = 32
	mitmConnectionLimit    = 8192
	mitmDurationSampleCap  = 4096
	mitmCorrelationWindowS = 30
)

type MitmDiagnostics struct {
	Observed           MitmObserved           `json:"observed"`
	RelatedUnconfirmed MitmRelatedUnconfirmed `json:"related_unconfirmed"`
	Unknown            MitmUnknown            `json:"unknown"`
	Notes              []string               `json:"notes,omitempty"`
}

type MitmObserved struct {
	TLSHandshakeFailed        []TLSHandshakeCount   `json:"tls_handshake_failed"`
	ConnectDecided            ConnectDecisionStats  `json:"connect_decided"`
	MitmWithoutBackendForward []MitmWithoutForward  `json:"mitm_without_backend_forward"`
	BackendForwardFinished    []BackendForwardStat  `json:"backend_forward_finished"`
	HostTrafficClasses        []HostTrafficClass    `json:"host_traffic_classes"`
	MixedHosts                []string              `json:"mixed_hosts"`
	TLSReclassified           []TLSBucketCount      `json:"tls_reclassified"`
	CorrelationChains         CorrelationChainStats `json:"correlation_chains"`
	UnknownTraffic            UnknownTrafficStats   `json:"unknown_traffic"`
}

type TLSHandshakeCount struct {
	Host          string `json:"host"`
	Direction     string `json:"direction"`
	TLSRole       string `json:"tls_role"`
	ErrorCategory string `json:"error_category"`
	Count         int    `json:"count"`
}

type ConnectDecisionStats struct {
	Mitm             int     `json:"mitm"`
	Passthrough      int     `json:"passthrough"`
	Other            int     `json:"other,omitempty"`
	Total            int     `json:"total"`
	MitmRatio        float64 `json:"mitm_ratio"`
	PassthroughRatio float64 `json:"passthrough_ratio"`
}

type MitmWithoutForward struct {
	Host         string `json:"host"`
	ConnectionID string `json:"connection_id,omitempty"`
}

type BackendForwardStat struct {
	TrafficClass  string  `json:"traffic_class"`
	Status        string  `json:"status,omitempty"`
	StatusCode    int     `json:"status_code"`
	Count         int     `json:"count"`
	SuccessCount  int     `json:"success_count"`
	SuccessRate   float64 `json:"success_rate"`
	DurationP50MS int64   `json:"duration_p50_ms"`
	DurationP95MS int64   `json:"duration_p95_ms"`
}

type HostTrafficClass struct {
	Host           string   `json:"host"`
	TrafficClasses []string `json:"traffic_classes"`
	HTTPEventCount int      `json:"http_event_count"`
}

type TLSBucketCount struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

type CorrelationChainStats struct {
	ConnectionsObserved     int `json:"connections_observed"`
	Complete                int `json:"complete"`
	MissingHTTPRequestID    int `json:"missing_http_request_id"`
	MissingTraceID          int `json:"missing_trace_id"`
	MissingBackendRoute     int `json:"missing_backend_route"`
	HTTPWithoutConnectionID int `json:"http_without_connection_id"`
}

type UnknownTrafficStats struct {
	HTTPEvents           int      `json:"http_events"`
	UnknownEvents        int      `json:"unknown_events"`
	UnknownRatio         float64  `json:"unknown_ratio"`
	SanitizedPathSamples []string `json:"sanitized_path_samples"`
}

type MitmRelatedUnconfirmed struct {
	HostOnlyMitmWithoutForward []MitmWithoutForward `json:"host_only_mitm_without_forward"`
	TLSNearProviderFailures    []TimeCorrelation    `json:"tls_near_provider_failures"`
	Note                       string               `json:"note"`
}

type TimeCorrelation struct {
	Host             string `json:"host,omitempty"`
	WindowStartUnix  int64  `json:"window_start_unix"`
	TLSCount         int    `json:"tls_count"`
	ProviderFailures int    `json:"provider_failures"`
}

type MitmUnknown struct {
	HandshakeWithoutHTTPPath int      `json:"handshake_without_http_path"`
	ConnectUnknownClass      int      `json:"connect_unknown_traffic_class"`
	EventsMissingNewFields   int      `json:"events_missing_new_fields"`
	IncompleteChains         int      `json:"incomplete_correlation_chains"`
	Notes                    []string `json:"notes,omitempty"`
}

type tlsKey struct {
	host, direction, role, category string
}

type forwardKey struct {
	class  string
	status string
	code   int
}

type connState struct {
	host          string
	action        string
	httpRequestID string
	traceID       string
	route         string
	hasForward    bool
	seenHTTP      bool
}

type hostHTTPState struct {
	classes map[string]struct{}
	count   int
}

type windowPair struct {
	tls      int
	provider int
	host     string
}

type mitmReducer struct {
	tlsCounts        map[tlsKey]int
	tlsBuckets       map[string]int
	connect          ConnectDecisionStats
	conns            map[string]*connState
	connOverflow     bool
	hostMITM         map[string]int
	hostForward      map[string]int
	hostHTTP         map[string]*hostHTTPState
	forwardStats     map[forwardKey]*forwardAgg
	pathSamples      map[string]struct{}
	httpEvents       int
	unknownHTTP      int
	handshakeNoPath  int
	connectUnknown   int
	missingFields    int
	httpNoConnection int
	tlsWindows       map[int64]map[string]int
	providerWindows  map[int64]map[string]int
}

type forwardAgg struct {
	count    int
	success  int
	duration []int64
}

func newMitmReducer() *mitmReducer {
	return &mitmReducer{
		tlsCounts:       make(map[tlsKey]int),
		tlsBuckets:      make(map[string]int),
		conns:           make(map[string]*connState),
		hostMITM:        make(map[string]int),
		hostForward:     make(map[string]int),
		hostHTTP:        make(map[string]*hostHTTPState),
		forwardStats:    make(map[forwardKey]*forwardAgg),
		pathSamples:     make(map[string]struct{}),
		tlsWindows:      make(map[int64]map[string]int),
		providerWindows: make(map[int64]map[string]int),
	}
}

func MitmFromEvents(events []workspace.EventRecord) MitmDiagnostics {
	reducer := newMitmReducer()
	for index := range events {
		reducer.add(events[index])
	}
	return reducer.snapshot()
}

func MitmFromWorkspace(ctx context.Context, store *workspace.Workspace, datasetID int64) (MitmDiagnostics, error) {
	reducer := newMitmReducer()
	var after *workspace.EventCursor
	for {
		rows, err := store.ListGlobalEvents(ctx, datasetID, after, eventBatchLimit)
		if err != nil {
			return MitmDiagnostics{}, err
		}
		if len(rows) == 0 {
			break
		}
		for index := range rows {
			reducer.add(rows[index].EventRecord)
		}
		last := rows[len(rows)-1].Cursor
		after = &last
	}
	return reducer.snapshot(), nil
}

func (reducer *mitmReducer) add(event workspace.EventRecord) {
	fields := parseSafeFields(event.SafeFieldsJSON)
	host := firstNonEmpty(fieldString(fields, "host"), fieldString(fields, "target_host"), sanitize.Host(event.Route))
	action := strings.ToLower(fieldString(fields, "action"))
	tlsRole := fieldString(fields, "tls_role")
	trafficClass := firstNonEmpty(fieldString(fields, "traffic_class"), "unknown")
	connectionID := fieldString(fields, "connection_id")
	path := sanitize.Path(firstNonEmpty(fieldString(fields, "path"), event.Route))
	direction := strings.TrimSpace(event.Direction)
	name := strings.TrimSpace(event.Event)

	if isMitmDiagnosticEvent(name) && missingMitmFields(event, fields, host, action, tlsRole, trafficClass) {
		reducer.missingFields++
	}

	switch name {
	case "tls_handshake_failed":
		key := tlsKey{
			host:      firstNonEmpty(host, "unknown"),
			direction: firstNonEmpty(direction, "unknown"),
			role:      firstNonEmpty(tlsRole, "unknown"),
			category:  firstNonEmpty(event.ErrorCategory, "unknown"),
		}
		reducer.tlsCounts[key]++
		reducer.tlsBuckets[ReclassifyTLS(event.ErrorCategory)]++
		if path == "" || path == "/" || trafficClass == "unknown" {
			reducer.handshakeNoPath++
		}
		reducer.noteTLSWindow(event.Timestamp, host)
		reducer.touchConn(connectionID, host, action, event, path, false)
	case "connect_decided":
		switch action {
		case "mitm":
			reducer.connect.Mitm++
			if host != "" {
				reducer.hostMITM[host]++
			}
		case "passthrough":
			reducer.connect.Passthrough++
		default:
			reducer.connect.Other++
		}
		if trafficClass == "unknown" || path == "" || path == "/" {
			reducer.connectUnknown++
		}
		reducer.touchConn(connectionID, host, action, event, path, false)
	case "backend_forward_started", "backend_forward_finished":
		if host != "" {
			reducer.hostForward[host]++
		}
		reducer.recordHTTPClass(host, trafficClass, path)
		reducer.touchConn(connectionID, host, firstNonEmpty(action, "backend_forward"), event, path, true)
		if connectionID == "" {
			reducer.httpNoConnection++
		}
		if name == "backend_forward_finished" {
			code, _ := fieldInt(fields, "status_code")
			key := forwardStatusKey(firstNonEmpty(trafficClass, "unknown"), event, code)
			agg := reducer.forwardStats[key]
			if agg == nil {
				agg = &forwardAgg{}
				reducer.forwardStats[key] = agg
			}
			agg.count++
			if isForwardSuccess(event, code) {
				agg.success++
			}
			if event.DurationMS > 0 && len(agg.duration) < mitmDurationSampleCap {
				agg.duration = append(agg.duration, event.DurationMS)
			}
		}
	default:
		if event.Layer == "mitm" && (strings.Contains(name, "request") || strings.Contains(name, "intercept")) {
			reducer.recordHTTPClass(host, trafficClass, path)
			reducer.touchConn(connectionID, host, action, event, path, false)
		}
	}

	if isProviderFailure(event) {
		reducer.noteProviderWindow(event.Timestamp, host)
	}
}

func (reducer *mitmReducer) recordHTTPClass(host, trafficClass, path string) {
	reducer.httpEvents++
	class := firstNonEmpty(trafficClass, "unknown")
	if class == "unknown" {
		reducer.unknownHTTP++
		reducer.addPathSample(path)
	}
	if host == "" {
		return
	}
	state := reducer.hostHTTP[host]
	if state == nil {
		state = &hostHTTPState{classes: make(map[string]struct{})}
		reducer.hostHTTP[host] = state
	}
	state.count++
	if class != "unknown" {
		state.classes[class] = struct{}{}
	} else if path != "" && path != "/" {
		state.classes[class] = struct{}{}
	}
}

func (reducer *mitmReducer) touchConn(connectionID, host, action string, event workspace.EventRecord, path string, forward bool) {
	if connectionID == "" {
		return
	}
	state := reducer.conns[connectionID]
	if state == nil {
		if len(reducer.conns) >= mitmConnectionLimit {
			reducer.connOverflow = true
			return
		}
		state = &connState{}
		reducer.conns[connectionID] = state
	}
	if host != "" {
		state.host = host
	}
	if action == "mitm" || action == "passthrough" || state.action == "" {
		if action != "" {
			state.action = action
		}
	}
	if event.HTTPRequestID != "" {
		state.httpRequestID = event.HTTPRequestID
		state.seenHTTP = true
	}
	if strings.TrimSpace(event.TraceID) != "" && !strings.HasPrefix(event.TraceKey, "orphan:") {
		state.traceID = strings.TrimSpace(event.TraceID)
	}
	if path != "" && path != "/" {
		state.route = path
		state.seenHTTP = true
	} else if strings.TrimSpace(event.Route) != "" {
		state.route = sanitize.Path(event.Route)
		if state.route != "" && state.route != "/" {
			state.seenHTTP = true
		}
	}
	if forward {
		state.hasForward = true
		state.seenHTTP = true
	}
}

func (reducer *mitmReducer) noteTLSWindow(when time.Time, host string) {
	if when.IsZero() {
		return
	}
	window := when.UTC().Unix() / mitmCorrelationWindowS
	hosts := reducer.tlsWindows[window]
	if hosts == nil {
		hosts = make(map[string]int)
		reducer.tlsWindows[window] = hosts
	}
	hosts[firstNonEmpty(host, "")]++
}

func (reducer *mitmReducer) noteProviderWindow(when time.Time, host string) {
	if when.IsZero() {
		return
	}
	window := when.UTC().Unix() / mitmCorrelationWindowS
	hosts := reducer.providerWindows[window]
	if hosts == nil {
		hosts = make(map[string]int)
		reducer.providerWindows[window] = hosts
	}
	hosts[firstNonEmpty(host, "")]++
}

func (reducer *mitmReducer) snapshot() MitmDiagnostics {
	observed := MitmObserved{
		TLSHandshakeFailed:        reducer.tlsCountSlice(),
		ConnectDecided:            reducer.connectStats(),
		MitmWithoutBackendForward: reducer.mitmWithoutForwardObserved(),
		BackendForwardFinished:    reducer.forwardSlice(),
		HostTrafficClasses:        reducer.hostClassSlice(),
		MixedHosts:                reducer.mixedHosts(),
		TLSReclassified:           reducer.tlsBucketSlice(),
		CorrelationChains:         reducer.chainStats(),
		UnknownTraffic:            reducer.unknownTraffic(),
	}
	related := MitmRelatedUnconfirmed{
		HostOnlyMitmWithoutForward: reducer.mitmWithoutForwardHostOnly(),
		TLSNearProviderFailures:    reducer.timeCorrelations(),
		Note:                       "时间邻近的 TLS 失败与 provider 失败只构成相关但未证实，不能据此归因成 provider 故障。",
	}
	unknown := MitmUnknown{
		HandshakeWithoutHTTPPath: reducer.handshakeNoPath,
		ConnectUnknownClass:      reducer.connectUnknown,
		EventsMissingNewFields:   reducer.missingFields,
		IncompleteChains:         observed.CorrelationChains.MissingHTTPRequestID + observed.CorrelationChains.MissingTraceID + observed.CorrelationChains.MissingBackendRoute + observed.CorrelationChains.HTTPWithoutConnectionID,
		Notes: []string{
			"CONNECT/TLS 阶段没有 HTTP path，traffic_class 只能记为 unknown，不能归因到具体业务。",
			"旧日志缺少 connection_id/traffic_class/tls_role 时按 unknown 处理，不回填伪造关联。",
		},
	}
	if reducer.connOverflow {
		unknown.Notes = append(unknown.Notes, "connection_id 集合超过分析上限，部分连接未纳入明细。")
	}
	return MitmDiagnostics{
		Observed:           observed,
		RelatedUnconfirmed: related,
		Unknown:            unknown,
		Notes: []string{
			"已观测结论只来自事件直接字段。",
			"tls_handshake_failed 不按时间窗口推断成 provider 故障。",
		},
	}
}

func (reducer *mitmReducer) connectStats() ConnectDecisionStats {
	stats := reducer.connect
	stats.Total = stats.Mitm + stats.Passthrough + stats.Other
	if stats.Total > 0 {
		stats.MitmRatio = float64(stats.Mitm) / float64(stats.Total)
		stats.PassthroughRatio = float64(stats.Passthrough) / float64(stats.Total)
	}
	return stats
}

func (reducer *mitmReducer) tlsCountSlice() []TLSHandshakeCount {
	items := make([]TLSHandshakeCount, 0, len(reducer.tlsCounts))
	for key, count := range reducer.tlsCounts {
		items = append(items, TLSHandshakeCount{
			Host: key.host, Direction: key.direction, TLSRole: key.role,
			ErrorCategory: key.category, Count: count,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		if items[i].Host != items[j].Host {
			return items[i].Host < items[j].Host
		}
		if items[i].Direction != items[j].Direction {
			return items[i].Direction < items[j].Direction
		}
		if items[i].TLSRole != items[j].TLSRole {
			return items[i].TLSRole < items[j].TLSRole
		}
		return items[i].ErrorCategory < items[j].ErrorCategory
	})
	return items
}

func (reducer *mitmReducer) tlsBucketSlice() []TLSBucketCount {
	order := []string{"client_unknown_ca", "upstream_tls", "protocol", "backend", "other"}
	items := make([]TLSBucketCount, 0, len(order))
	for _, bucket := range order {
		if count := reducer.tlsBuckets[bucket]; count > 0 {
			items = append(items, TLSBucketCount{Bucket: bucket, Count: count})
		}
	}
	return items
}

func (reducer *mitmReducer) mitmWithoutForwardObserved() []MitmWithoutForward {
	items := make([]MitmWithoutForward, 0)
	for connectionID, state := range reducer.conns {
		if state.action != "mitm" || state.hasForward {
			continue
		}
		items = append(items, MitmWithoutForward{Host: firstNonEmpty(state.host, "unknown"), ConnectionID: connectionID})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Host != items[j].Host {
			return items[i].Host < items[j].Host
		}
		return items[i].ConnectionID < items[j].ConnectionID
	})
	if len(items) > mitmSampleLimit {
		items = items[:mitmSampleLimit]
	}
	return items
}

func (reducer *mitmReducer) mitmWithoutForwardHostOnly() []MitmWithoutForward {
	items := make([]MitmWithoutForward, 0)
	for host, mitmCount := range reducer.hostMITM {
		if mitmCount == 0 || reducer.hostForward[host] > 0 {
			continue
		}
		hasConnEvidence := false
		for _, state := range reducer.conns {
			if state.host == host && state.action == "mitm" {
				hasConnEvidence = true
				break
			}
		}
		if hasConnEvidence {
			continue
		}
		items = append(items, MitmWithoutForward{Host: host})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Host < items[j].Host })
	if len(items) > mitmSampleLimit {
		items = items[:mitmSampleLimit]
	}
	return items
}

func (reducer *mitmReducer) forwardSlice() []BackendForwardStat {
	items := make([]BackendForwardStat, 0, len(reducer.forwardStats))
	for key, agg := range reducer.forwardStats {
		stat := BackendForwardStat{
			TrafficClass: key.class, Status: key.status, StatusCode: key.code,
			Count: agg.count, SuccessCount: agg.success,
		}
		if agg.count > 0 {
			stat.SuccessRate = float64(agg.success) / float64(agg.count)
		}
		sorted := append([]int64(nil), agg.duration...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		stat.DurationP50MS = percentileMS(sorted, 50)
		stat.DurationP95MS = percentileMS(sorted, 95)
		items = append(items, stat)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TrafficClass != items[j].TrafficClass {
			return items[i].TrafficClass < items[j].TrafficClass
		}
		if items[i].StatusCode != items[j].StatusCode {
			return items[i].StatusCode < items[j].StatusCode
		}
		return items[i].Status < items[j].Status
	})
	return items
}

func (reducer *mitmReducer) hostClassSlice() []HostTrafficClass {
	items := make([]HostTrafficClass, 0, len(reducer.hostHTTP))
	for host, state := range reducer.hostHTTP {
		classes := sortedKeys(state.classes)
		items = append(items, HostTrafficClass{Host: host, TrafficClasses: classes, HTTPEventCount: state.count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Host < items[j].Host })
	return items
}

func (reducer *mitmReducer) mixedHosts() []string {
	hosts := make([]string, 0)
	for host, state := range reducer.hostHTTP {
		if len(state.classes) > 1 {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func (reducer *mitmReducer) chainStats() CorrelationChainStats {
	stats := CorrelationChainStats{
		ConnectionsObserved:     len(reducer.conns),
		HTTPWithoutConnectionID: reducer.httpNoConnection,
	}
	for _, state := range reducer.conns {
		if !state.seenHTTP {
			continue
		}
		missingHTTP := state.httpRequestID == ""
		missingTrace := state.traceID == ""
		missingRoute := state.route == "" || state.route == "/"
		if !missingHTTP && !missingTrace && !missingRoute {
			stats.Complete++
			continue
		}
		if missingHTTP {
			stats.MissingHTTPRequestID++
		}
		if missingTrace {
			stats.MissingTraceID++
		}
		if missingRoute {
			stats.MissingBackendRoute++
		}
	}
	return stats
}

func (reducer *mitmReducer) unknownTraffic() UnknownTrafficStats {
	stats := UnknownTrafficStats{
		HTTPEvents:           reducer.httpEvents,
		UnknownEvents:        reducer.unknownHTTP,
		SanitizedPathSamples: sortedKeys(reducer.pathSamples),
	}
	if stats.HTTPEvents > 0 {
		stats.UnknownRatio = float64(stats.UnknownEvents) / float64(stats.HTTPEvents)
	}
	return stats
}

func (reducer *mitmReducer) timeCorrelations() []TimeCorrelation {
	items := make([]TimeCorrelation, 0)
	for window, tlsHosts := range reducer.tlsWindows {
		providerHosts := reducer.providerWindows[window]
		if len(providerHosts) == 0 {
			continue
		}
		hosts := make(map[string]struct{})
		for host := range tlsHosts {
			hosts[host] = struct{}{}
		}
		for host := range providerHosts {
			hosts[host] = struct{}{}
		}
		for host := range hosts {
			tlsCount := tlsHosts[host]
			providerCount := providerHosts[host]
			if host == "" {
				tlsCount = sumCounts(tlsHosts)
				providerCount = sumCounts(providerHosts)
			} else if tlsCount == 0 || providerCount == 0 {
				continue
			}
			if tlsCount == 0 || providerCount == 0 {
				continue
			}
			items = append(items, TimeCorrelation{
				Host: host, WindowStartUnix: window * mitmCorrelationWindowS,
				TLSCount: tlsCount, ProviderFailures: providerCount,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].WindowStartUnix != items[j].WindowStartUnix {
			return items[i].WindowStartUnix < items[j].WindowStartUnix
		}
		return items[i].Host < items[j].Host
	})
	if len(items) > mitmSampleLimit {
		items = items[:mitmSampleLimit]
	}
	return items
}

func (reducer *mitmReducer) addPathSample(path string) {
	sample := sanitize.Path(path)
	if sample == "" || sample == "/" {
		return
	}
	if _, exists := reducer.pathSamples[sample]; exists {
		return
	}
	if len(reducer.pathSamples) < mitmSampleLimit {
		reducer.pathSamples[sample] = struct{}{}
		return
	}
	max := ""
	for existing := range reducer.pathSamples {
		if existing > max {
			max = existing
		}
	}
	if sample < max {
		delete(reducer.pathSamples, max)
		reducer.pathSamples[sample] = struct{}{}
	}
}

func forwardStatusKey(trafficClass string, event workspace.EventRecord, code int) forwardKey {
	class := firstNonEmpty(trafficClass, "unknown")
	if code > 0 {
		return forwardKey{class: class, status: strconv.Itoa(code), code: code}
	}
	return forwardKey{class: class, status: firstNonEmpty(event.Status, "unknown"), code: 0}
}

func parseSafeFields(payload string) map[string]any {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return nil
	}
	return sanitize.AllowlistedFields(fields)
}

func fieldString(fields map[string]any, key string) string {
	if len(fields) == 0 {
		return ""
	}
	value, ok := fields[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(stringifyScalar(typed))
	}
}

func fieldInt(fields map[string]any, key string) (int, bool) {
	if len(fields) == 0 {
		return 0, false
	}
	value, ok := fields[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func stringifyScalar(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(strings.Trim(strings.ReplaceAll(toString(typed), "\"", ""), " "))
	}
}

func toString(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(payload)
}

func isMitmDiagnosticEvent(name string) bool {
	switch name {
	case "tls_handshake_failed", "connect_decided", "backend_forward_started", "backend_forward_finished":
		return true
	default:
		return false
	}
}

func missingMitmFields(event workspace.EventRecord, fields map[string]any, host, action, tlsRole, trafficClass string) bool {
	if event.Event == "tls_handshake_failed" {
		return host == "" || tlsRole == "" || event.Direction == "" || event.ErrorCategory == ""
	}
	if event.Event == "connect_decided" {
		return host == "" || action == ""
	}
	if event.Event == "backend_forward_started" || event.Event == "backend_forward_finished" {
		return host == "" || trafficClass == "" || trafficClass == "unknown" && fieldString(fields, "traffic_class") == ""
	}
	return false
}

func ReclassifyTLS(category string) string {
	switch strings.TrimSpace(category) {
	case "client_unknown_ca":
		return "client_unknown_ca"
	case "upstream_unknown_ca", "upstream_remote_unknown_certificate", "upstream_tls_handshake_failed", "hostname_mismatch":
		return "upstream_tls"
	case "handshake_mismatch", "upstream_http2", "mitm_tls_config_failed":
		return "protocol"
	case "backend_unavailable", "backend":
		return "backend"
	default:
		if strings.Contains(category, "backend") {
			return "backend"
		}
		if strings.Contains(category, "upstream") || strings.Contains(category, "tls") {
			if strings.Contains(category, "client") {
				return "other"
			}
			return "upstream_tls"
		}
		return "other"
	}
}

func isForwardSuccess(event workspace.EventRecord, statusCode int) bool {
	if statusCode >= 200 && statusCode < 300 {
		return true
	}
	if statusCode > 0 {
		return false
	}
	if isSemanticFailure(event.SemanticOutcome) || strings.EqualFold(event.Status, "error") || event.ErrorCategory != "" {
		return false
	}
	return isTechnicalSuccess(event.Status) || event.SemanticOutcome == "succeeded"
}

func isProviderFailure(event workspace.EventRecord) bool {
	if event.Event == "tls_handshake_failed" {
		return false
	}
	failed := isSemanticFailure(event.SemanticOutcome) || strings.EqualFold(event.Status, "error") || event.ErrorCategory != ""
	if !failed {
		return false
	}
	return event.Capability == "provider" || event.Layer == "provider" || strings.Contains(event.Event, "provider")
}

func percentileMS(sorted []int64, percent int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	for index, value := range sorted {
		rank := index + 1
		if rank*100 >= len(sorted)*percent {
			return value
		}
	}
	return sorted[len(sorted)-1]
}

func sumCounts(values map[string]int) int {
	total := 0
	for _, count := range values {
		total += count
	}
	return total
}

func RedactMitmDiagnostics(input MitmDiagnostics, pseudonymize func(string) string) MitmDiagnostics {
	if pseudonymize == nil {
		return input
	}
	redactList := func(items []MitmWithoutForward) []MitmWithoutForward {
		out := make([]MitmWithoutForward, len(items))
		for index, item := range items {
			item.ConnectionID = pseudonymize(item.ConnectionID)
			out[index] = item
		}
		return out
	}
	input.Observed.MitmWithoutBackendForward = redactList(input.Observed.MitmWithoutBackendForward)
	input.RelatedUnconfirmed.HostOnlyMitmWithoutForward = redactList(input.RelatedUnconfirmed.HostOnlyMitmWithoutForward)
	for index, sample := range input.Observed.UnknownTraffic.SanitizedPathSamples {
		input.Observed.UnknownTraffic.SanitizedPathSamples[index] = sanitize.Path(sample)
	}
	return input
}
