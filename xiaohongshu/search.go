package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// filterGroup 面板上的一个筛选组：标签是什么、对应入参的哪个字段、允许哪些取值。
//
// 组和选项一律按文本定位，不用序号。面板里同一个选项可能渲染成多个 div.tags
// （数量随视口而变），首项是否重复各组也不一致，下标对不齐。
type filterGroup struct {
	label   string                    // 面板上这一组的标签文本
	pick    func(FilterOption) string // 从入参里取这一组的值
	allowed []string                  // 合法取值；在打开页面之前就能挡掉写错的值
}

var filterGroups = []filterGroup{
	{"排序依据", func(f FilterOption) string { return f.SortBy },
		[]string{"综合", "最新", "最多点赞", "最多评论", "最多收藏"}},
	{"笔记类型", func(f FilterOption) string { return f.NoteType },
		[]string{"不限", "视频", "图文"}},
	{"发布时间", func(f FilterOption) string { return f.PublishTime },
		[]string{"不限", "一天内", "一周内", "半年内"}},
	{"搜索范围", func(f FilterOption) string { return f.SearchScope },
		[]string{"不限", "已看过", "未看过", "已关注"}},
	{"位置距离", func(f FilterOption) string { return f.Location },
		[]string{"不限", "同城", "附近"}},
}

// pendingFilter 一个待应用的筛选项。
type pendingFilter struct {
	group  string // 组标签
	option string // 选项文本
}

// collectFilters 把入参展开成待应用的筛选项，顺便校验取值。
//
// 校验放在这里是为了在打开浏览器之前就挡掉写错的值——否则要等导航、悬停、
// 在面板里找不到之后才能报错，等于为了说一句"你写错了"先向平台发一次请求。
func collectFilters(filters []FilterOption) ([]pendingFilter, error) {
	var pending []pendingFilter

	for _, f := range filters {
		for _, g := range filterGroups {
			value := g.pick(f)
			if value == "" {
				continue
			}
			if !slices.Contains(g.allowed, value) {
				return nil, fmt.Errorf("%s 不支持 %q，可选：%s",
					g.label, value, strings.Join(g.allowed, "、"))
			}
			pending = append(pending, pendingFilter{group: g.label, option: value})
		}
	}

	return pending, nil
}

type SearchAction struct {
	page *rod.Page
}

func NewSearchAction(page *rod.Page) *SearchAction {
	return &SearchAction{page: page}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	// 先校验筛选取值，必须在导航之前——写错的值不该先向平台发一次请求再报错。
	pending, err := collectFilters(filters)
	if err != nil {
		return nil, err
	}

	// 搜索必须在调用方的 30s HTTP 超时前返回；导航与数据读取共用期限。
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	page := s.page.Context(ctx)
	network, stopObserving := observeSearchNetwork(page)
	defer stopObserving()
	observe := func() (searchFeedState, error) {
		state, err := readSearchFeeds(page)
		state.Network = network
		return state, err
	}

	searchURL := makeSearchURL(keyword)
	feeds, err := waitSearchFeeds(ctx,
		func() error { return page.Navigate(searchURL) },
		observe,
	)
	if err != nil {
		return nil, err
	}
	humanize.Delay(ctx, humanize.AfterNavigate)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search result deadline: %w", err)
	}

	if len(pending) > 0 {
		// 悬停在筛选按钮上展开面板
		filterButton, err := page.Element(`div.filter`)
		if err != nil {
			return nil, &searchStageError{"等待筛选按钮失败", err}
		}
		if err := humanize.Hover(filterButton); err != nil {
			return nil, &searchStageError{"悬停筛选按钮失败", err}
		}
		humanize.Delay(ctx, humanize.BeforeClick)

		// 等待筛选面板出现
		if err := page.Wait(rod.Eval(`() => document.querySelector('div.filter-panel') !== null`)); err != nil {
			return nil, &searchStageError{"等待筛选面板失败", err}
		}

		// 记下筛选前的结果，用来判断筛选后的数据什么时候到位
		before := readFeedIDs(page)

		// 用 ClickNoWait：筛选面板是 hover 浮层，rod 的 WaitInteractable 会误判被遮挡而死等；
		// ClickNoWait 移进面板内选项（维持 hover、面板不关）再点。
		for _, pf := range pending {
			option, err := findFilterOption(page, pf)
			if err != nil {
				return nil, &searchStageError{"查找筛选选项失败", err}
			}
			humanize.Delay(ctx, humanize.BeforeClick)
			if err := humanize.ClickNoWait(option); err != nil {
				return nil, &searchStageError{"点击筛选选项失败", err}
			}
		}

		if err := waitFeedsChanged(ctx, before, 15*time.Second, func() string { return readFeedIDs(page) }); err != nil {
			return nil, err
		}
		feeds, err = waitSearchFeeds(ctx, func() error { return nil },
			observe)
		if err != nil {
			return nil, err
		}
	}

	return onlyNotes(feeds), nil
}

type searchFeedState struct {
	Ready           bool                      `json:"ready"`
	Feeds           []Feed                    `json:"feeds"`
	Origin          string                    `json:"origin"`
	Path            string                    `json:"path"`
	HasInitialState bool                      `json:"hasInitialState"`
	HasSearch       bool                      `json:"hasSearch"`
	HasFeeds        bool                      `json:"hasFeeds"`
	FeedType        string                    `json:"feedType"`
	FeedCount       int                       `json:"feedCount"`
	SearchFields    map[string]string         `json:"searchFields"`
	SearchBooleans  map[string]bool           `json:"searchBooleans"`
	Network         *searchNetworkDiagnostics `json:"-"`
}

// 保留 errors.Is 的原因链，但 HTTP 错误中不展开浏览器异常或 DOM。
type searchStageError struct {
	stage string
	cause error
}

func (e *searchStageError) Error() string { return e.stage }
func (e *searchStageError) Unwrap() error { return e.cause }

// 只等待所需的搜索数据；图片、埋点和页面动画不影响可读结果。
const searchFeedStateJS = `() => {
	const search = window.__INITIAL_STATE__?.search;
	const feeds = search?.feeds;
	const data = Array.isArray(feeds) ? feeds
		: feeds && (feeds.value !== undefined ? feeds.value : feeds._value);
	const type = value => value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value;
	// 只观察实际字段名、类型及布尔值，不假定某个字段代表加载完成。
	const searchFields = {}, searchBooleans = {};
	if (search && typeof search === 'object') {
		for (const key of Object.keys(search).filter(key => /^[A-Za-z_$][A-Za-z0-9_$]{0,47}$/.test(key)).sort().slice(0, 64)) {
			try {
				let value = search[key];
				if (value && typeof value === 'object' && !Array.isArray(value)) {
					if (value.value !== undefined) value = value.value;
					else if (value._value !== undefined) value = value._value;
				}
				searchFields[key] = type(value);
				if (typeof value === 'boolean') searchBooleans[key] = value;
			} catch (_) { searchFields[key] = 'unreadable'; }
		}
	}
	// 空数组可能只是初始化，不能证明搜索已完成。
	return JSON.stringify({ready:Array.isArray(data) && data.length > 0, feeds:Array.isArray(data) ? data : null,
		origin:window.location.origin, path:window.location.pathname,
		hasInitialState:window.__INITIAL_STATE__ !== undefined,
		hasSearch:search !== undefined, hasFeeds:feeds !== undefined,
		feedType:type(data), feedCount:Array.isArray(data) ? data.length : -1,
		searchFields, searchBooleans});
}`

func readSearchFeeds(page *rod.Page) (searchFeedState, error) {
	result, err := page.Eval(searchFeedStateJS)
	if err != nil {
		return searchFeedState{}, &searchStageError{"read search feeds failed", err}
	}
	var state searchFeedState
	if err := json.Unmarshal([]byte(result.Value.String()), &state); err != nil {
		return searchFeedState{}, &searchStageError{"decode search feeds failed", err}
	}
	return state, nil
}

func waitSearchFeeds(ctx context.Context, navigate func() error, observe func() (searchFeedState, error)) ([]Feed, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search cancelled: %w", err)
	}
	if err := navigate(); err != nil {
		return nil, &searchStageError{"navigate search failed", err}
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last searchFeedState
	for {
		if err := ctx.Err(); err != nil {
			return nil, searchNotReadyError(err, last)
		}
		state, err := observe()
		if err != nil {
			if ctx.Err() != nil {
				return nil, searchNotReadyError(ctx.Err(), last)
			}
			return nil, &searchStageError{"read search feeds failed", err}
		}
		last = state
		if err := ctx.Err(); err != nil {
			return nil, searchNotReadyError(err, last)
		}
		if state.Ready {
			if state.Feeds == nil {
				return nil, fmt.Errorf("search feeds are not an array")
			}
			if len(state.Feeds) > 0 {
				return state.Feeds, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, searchNotReadyError(ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

func searchNotReadyError(cause error, state searchFeedState) error {
	// 不记录 URL query、页面内容、账号或搜索结果。
	fields := logrus.Fields{
		"origin": state.Origin, "path": state.Path,
		"initial_state": state.HasInitialState, "search": state.HasSearch, "feeds": state.HasFeeds,
	}
	fieldTypes, booleans := safeSearchStateFields(state)
	fields["feeds_type"] = safeSearchValueType(state.FeedType)
	fields["feeds_count"] = state.FeedCount
	fields["search_fields"] = fieldTypes
	fields["search_booleans"] = booleans
	if state.Network != nil {
		count, requests := state.Network.snapshot()
		fields["search_request_count"] = count
		encoded, _ := json.Marshal(requests) // Fixed scalar-only schema, including an explicit empty array.
		fields["search_requests"] = string(encoded)
		enabled, total, scriptFailures := state.Network.startupSnapshot()
		fields["network_enable_status"] = enabled
		fields["page_request_count"] = total
		encoded, _ = json.Marshal(scriptFailures)
		fields["search_startup_script_failures"] = string(encoded)
	}
	logrus.WithFields(fields).Warn("search_data_not_ready")
	return fmt.Errorf("search feeds not ready (origin=%q path=%q initial_state=%t search=%t feeds=%t): %w",
		state.Origin, state.Path, state.HasInitialState, state.HasSearch, state.HasFeeds, cause)
}

// Only this page's search XHR/Fetch metadata is retained. Request IDs are local
// correlation keys and never logged; headers, bodies and error text are ignored.
type searchNetworkRequest struct {
	Origin        string `json:"origin"`
	Path          string `json:"path"`
	HTTPStatus    int    `json:"http_status"`
	Completed     bool   `json:"completed"`
	Failed        bool   `json:"failed"`
	Canceled      bool   `json:"canceled"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type searchNetworkDiagnostics struct {
	mu           sync.Mutex
	count        int
	order        []proto.NetworkRequestID
	requests     map[proto.NetworkRequestID]searchNetworkRequest
	enableStatus string
	total        int
	scriptOrder  []proto.NetworkRequestID
	scripts      map[proto.NetworkRequestID]searchNetworkRequest
}

func (d *searchNetworkDiagnostics) request(event *proto.NetworkRequestWillBeSent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.total++
	if event.Request != nil && event.Type == proto.NetworkResourceTypeScript {
		if address, ok := searchStartupScript(event.Request.URL); ok && len(d.scriptOrder) < 32 {
			if d.scripts == nil {
				d.scripts = make(map[proto.NetworkRequestID]searchNetworkRequest)
			}
			if _, exists := d.scripts[event.RequestID]; !exists {
				d.scriptOrder = append(d.scriptOrder, event.RequestID)
				d.scripts[event.RequestID] = address
			}
		}
	}
	if event.Request == nil || (event.Type != proto.NetworkResourceTypeXHR && event.Type != proto.NetworkResourceTypeFetch) {
		return
	}
	u, err := url.Parse(event.Request.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return
	}
	host := strings.ToLower(u.Hostname())
	if (host != "xiaohongshu.com" && !strings.HasSuffix(host, ".xiaohongshu.com")) ||
		!strings.Contains(strings.ToLower(u.Path), "search") {
		return
	}
	d.count++
	if d.requests == nil {
		d.requests = make(map[proto.NetworkRequestID]searchNetworkRequest)
	}
	if _, exists := d.requests[event.RequestID]; exists || len(d.order) >= 8 {
		return
	}
	d.order = append(d.order, event.RequestID)
	d.requests[event.RequestID] = searchNetworkRequest{Origin: u.Scheme + "://" + u.Host, Path: u.EscapedPath()}
}

func (d *searchNetworkDiagnostics) response(event *proto.NetworkResponseReceived) {
	if event.Response == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if value, exists := d.requests[event.RequestID]; exists {
		value.HTTPStatus = event.Response.Status
		d.requests[event.RequestID] = value
	}
	if value, exists := d.scripts[event.RequestID]; exists {
		value.HTTPStatus = event.Response.Status
		d.scripts[event.RequestID] = value
	}
}

func (d *searchNetworkDiagnostics) finished(event *proto.NetworkLoadingFinished) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if value, exists := d.requests[event.RequestID]; exists {
		value.Completed = true
		d.requests[event.RequestID] = value
	}
	if value, exists := d.scripts[event.RequestID]; exists {
		value.Completed = true
		d.scripts[event.RequestID] = value
	}
}

func (d *searchNetworkDiagnostics) failed(event *proto.NetworkLoadingFailed) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if value, exists := d.scripts[event.RequestID]; exists {
		value.Failed, value.Canceled = true, event.Canceled
		d.scripts[event.RequestID] = value
	}
	if value, exists := d.requests[event.RequestID]; exists {
		value.Failed, value.Canceled = true, event.Canceled
		reason := string(event.BlockedReason)
		switch reason {
		case "", "other", "csp", "mixed-content", "origin", "inspector", "subresource-filter", "content-type",
			"coep-frame-resource-needs-coep-header", "coop-sandboxed-iframe-cannot-navigate-to-coop-page",
			"corp-not-same-origin", "corp-not-same-site",
			"corp-not-same-origin-after-defaulted-to-same-origin-by-coep",
			"corp-not-same-origin-after-defaulted-to-same-origin-by-dip",
			"corp-not-same-origin-after-defaulted-to-same-origin-by-coep-and-dip":
			value.BlockedReason = reason
		default:
			value.BlockedReason = "other"
		}
		d.requests[event.RequestID] = value
	}
}

func (d *searchNetworkDiagnostics) snapshot() (int, []searchNetworkRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	requests := make([]searchNetworkRequest, 0, len(d.order))
	for _, id := range d.order {
		requests = append(requests, d.requests[id])
	}
	return d.count, requests
}

func observeSearchNetwork(page *rod.Page) (*searchNetworkDiagnostics, func()) {
	ctx, cancel := context.WithCancel(page.GetContext())
	diagnostics := &searchNetworkDiagnostics{}
	// EachEvent subscribes synchronously before navigation and filters SessionID.
	wait := page.Context(ctx).EachEvent(diagnostics.request, diagnostics.response, diagnostics.finished, diagnostics.failed)
	// EachEvent's automatic enable ignores errors; explicitly confirm the CDP ACK.
	diagnostics.enableStatus = searchNetworkEnableStatus((proto.NetworkEnable{}).Call(page.Context(ctx)))
	return diagnostics, runSearchNetworkListener(cancel, wait)
}

func searchNetworkEnableStatus(err error) string {
	switch {
	case err == nil:
		return "enabled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "enable_failed"
	}
}

// Public Search route startup dependencies observed in index.0280008a.js and
// bundler-runtime.e5dd7116.js. Accept changing build hashes, never arbitrary paths.
var searchStartupScriptPath = regexp.MustCompile(`^/formula-static/xhs-pc-web/public/resource/js/(?:bundler-runtime|vendor-dynamic|library-polyfill|library-lodash|vendor|index|async/(?:Search|WorldCupShared|157|8233|9220|799|8390|7279|2492))\.[a-f0-9]{8}\.js$`)

func searchStartupScript(address string) (searchNetworkRequest, bool) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "fe-static.xhscdn.com" ||
		!searchStartupScriptPath.MatchString(u.EscapedPath()) {
		return searchNetworkRequest{}, false
	}
	return searchNetworkRequest{Origin: "https://fe-static.xhscdn.com", Path: u.EscapedPath()}, true
}

func (d *searchNetworkDiagnostics) startupSnapshot() (string, int, []searchNetworkRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	failures := make([]searchNetworkRequest, 0)
	for _, id := range d.scriptOrder {
		value := d.scripts[id]
		if value.Failed || value.HTTPStatus >= 400 {
			failures = append(failures, value)
			if len(failures) == 8 {
				break
			}
		}
	}
	status := d.enableStatus
	if status == "" {
		status = "not_checked"
	}
	return status, d.total, failures
}

func runSearchNetworkListener(cancel context.CancelFunc, wait func()) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()
	return func() {
		cancel()
		<-done // Rod's event stream and domain restoration share the cancelled context.
	}
}

var searchStateFieldName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,47}$`)

func safeSearchValueType(value string) string {
	if slices.Contains([]string{"undefined", "null", "array", "object", "boolean", "string", "number", "bigint", "symbol", "function", "unreadable"}, value) {
		return value
	}
	return "unknown"
}

func safeSearchStateFields(state searchFeedState) (map[string]string, map[string]bool) {
	keys := make([]string, 0, len(state.SearchFields))
	for key := range state.SearchFields {
		if searchStateFieldName.MatchString(key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) > 64 {
		keys = keys[:64]
	}
	fields, booleans := make(map[string]string), make(map[string]bool)
	for _, key := range keys {
		fields[key] = safeSearchValueType(state.SearchFields[key])
		if value, exists := state.SearchBooleans[key]; exists && fields[key] == "boolean" {
			booleans[key] = value
		}
	}
	return fields, booleans
}

// feedIDsJS 读当前结果集的 id 列表，用来判断数据有没有换一批。
const feedIDsJS = `() => {
	const f = window.__INITIAL_STATE__?.search?.feeds;
	const v = f ? (f.value !== undefined ? f.value : f._value) : null;
	return v ? v.map(x => x.id).join(",") : "";
}`

func readFeedIDs(page *rod.Page) string {
	res, err := page.Eval(feedIDsJS)
	if err != nil {
		return ""
	}
	return res.Value.Str()
}

// waitFeedsChanged 等筛选后的数据到位。
//
// 点完筛选项之后不能立刻读结果：站点是先把 feeds 清空、再灌入新数据，
// 中间这段时间读到的要么是空，要么还是筛选前那一批。原先用
// MustWait(__INITIAL_STATE__ !== undefined) 等，而这个条件从首屏起就为真、
// 立即返回，等于没等——多个筛选项一起用时表现为只有一部分生效。
//
// 超时不报错：筛选已经点上了，宁可返回可能偏旧的数据，也不要整个搜索失败。
func waitFeedsChanged(ctx context.Context, before string, timeout time.Duration, read func() string) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("等待筛选结果取消: %w", err)
		}
		now := read()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("等待筛选结果取消: %w", err)
		}
		if now != "" && now != before {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待筛选结果取消: %w", ctx.Err())
		case <-deadline.C:
			logrus.Warnf("筛选后等待结果刷新超时（%s），返回的可能是筛选前的数据", timeout)
			return nil
		case <-ticker.C:
		}
	}
}

// findFilterOption 在筛选面板里定位一个选项：按标签找到组，再在组内按文本找选项。
//
// 全程不用序号。同一个选项在面板里可能渲染成多个 div.tags（数量随视口而变，
// 且首项是否重复各组不一致），下标对不齐；早前用 div.tags:nth-child(N) 会选错项。
// 多份重复的位置尺寸完全相同，取第一个点下去落在同一处。
//
// 作用域必须限定在 div.filter-panel 内且只认 div.tags：页面别处存在同文本的
// 可见元素（顶部频道栏的「图文」「视频」、标签「综合」），放宽会点错地方。
func findFilterOption(page *rod.Page, pf pendingFilter) (*rod.Element, error) {
	groups, err := page.Elements("div.filter-panel div.filters")
	if err != nil {
		return nil, fmt.Errorf("读取筛选面板失败: %w", err)
	}

	for _, group := range groups {
		// 组标签是 div.filters 下的直接子 span
		label, err := group.Element(":scope > span")
		if err != nil {
			continue
		}
		text, err := label.Text()
		if err != nil || strings.TrimSpace(text) != pf.group {
			continue
		}

		options, err := group.Elements("div.tags")
		if err != nil {
			return nil, fmt.Errorf("读取「%s」的选项失败: %w", pf.group, err)
		}

		var available []string
		for _, opt := range options {
			t, err := opt.Text()
			if err != nil {
				continue
			}
			t = strings.TrimSpace(t)
			if t == pf.option {
				return opt, nil
			}
			available = append(available, t)
		}
		return nil, fmt.Errorf("「%s」里没有选项「%s」，页面上是：%s",
			pf.group, pf.option, strings.Join(available, "、"))
	}

	return nil, fmt.Errorf("筛选面板里没有「%s」这一组", pf.group)
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_search_result_notes
	//https://www.xiaohongshu.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_explore_feed
	return fmt.Sprintf("https://www.xiaohongshu.com/search_result?%s", values.Encode())
}
