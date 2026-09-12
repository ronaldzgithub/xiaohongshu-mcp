package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/headless_browser"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
	"github.com/xpzouying/xiaohongshu-mcp/configs"
	"github.com/xpzouying/xiaohongshu-mcp/cookies"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/downloader"
	"github.com/xpzouying/xiaohongshu-mcp/pkg/xhsutil"
	"github.com/xpzouying/xiaohongshu-mcp/xiaohongshu"
)

// XiaohongshuService 小红书业务服务
type XiaohongshuService struct {
	logins loginSessions

	publishMu       sync.Mutex
	publishAttempts map[string]*publishAttempt
	processImage    func([]string) ([]string, error)
	publishImage    func(context.Context, xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error)
}

type publishAttempt struct {
	fingerprint string
	running     bool
	response    *PublishResponse
	err         error
}

var (
	ErrPublishIdentityRequired = errors.New("发布请求必须包含 request_id 和 idempotency_key")
	ErrPublishAttemptInFlight  = errors.New("相同幂等键的发布请求仍在执行；禁止重复提交")
	ErrPublishIdentityConflict = errors.New("相同幂等键绑定了不同的发布请求")
)

// externalActionsEnabled 是原生写操作的最后一道安全开关。
// Huaxiaobao 仍须在调用前校验具名批准；该开关本身不代表批准。
func externalActionsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XHS_ENABLE_EXTERNAL_ACTIONS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func requireExternalActionsEnabled() error {
	if !externalActionsEnabled() {
		return fmt.Errorf("外部动作默认关闭；仅在受控执行边界显式设置 XHS_ENABLE_EXTERNAL_ACTIONS=true 后允许")
	}
	return nil
}

// NewXiaohongshuService 创建小红书服务实例
func NewXiaohongshuService() *XiaohongshuService {
	service := &XiaohongshuService{
		publishAttempts: make(map[string]*publishAttempt),
	}
	service.processImage = service.processImages
	service.publishImage = service.publishContent
	return service
}

// PublishRequest 发布请求
type PublishRequest struct {
	RequestID      string   `json:"request_id"`
	IdempotencyKey string   `json:"idempotency_key"`
	Title          string   `json:"title" binding:"required"`
	Content        string   `json:"content" binding:"required"`
	Images         []string `json:"images" binding:"required,min=1"`
	Tags           []string `json:"tags,omitempty"`
	ScheduleAt     string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	IsOriginal     bool     `json:"is_original,omitempty"` // 是否声明原创
	Visibility     string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products       []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// LoginStatusResponse 登录状态响应
type LoginStatusResponse struct {
	IsLoggedIn bool   `json:"is_logged_in"`
	Username   string `json:"username,omitempty"` // 当前登录账号的昵称
	UserID     string `json:"user_id,omitempty"`  // 用户唯一标识（个人主页 URL 中的 ID）
}

// LoginQrcodeResponse 登录扫码二维码
type LoginQrcodeResponse struct {
	Timeout    string `json:"timeout"`
	IsLoggedIn bool   `json:"is_logged_in"`
	Img        string `json:"img,omitempty"`
}

// PublishResponse 发布响应
type PublishResponse struct {
	RequestID    string `json:"request_id"`
	FeedID       string `json:"feed_id"`
	XsecToken    string `json:"xsec_token,omitempty"`
	EvidenceURL  string `json:"evidence_url"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Images       int    `json:"images"`
	Status       string `json:"status"`
	ScheduledFor string `json:"scheduled_for,omitempty"`
}

// PublishVideoRequest 发布视频请求（仅支持本地单个视频文件）
type PublishVideoRequest struct {
	Title      string   `json:"title" binding:"required"`
	Content    string   `json:"content" binding:"required"`
	Video      string   `json:"video" binding:"required"`
	Tags       []string `json:"tags,omitempty"`
	ScheduleAt string   `json:"schedule_at,omitempty"` // 定时发布时间，ISO8601格式，为空则立即发布
	Visibility string   `json:"visibility,omitempty"`  // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products   []string `json:"products,omitempty"`    // 商品关键词列表，用于绑定带货商品
}

// PublishVideoResponse 发布视频响应
type PublishVideoResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Video   string `json:"video"`
	Status  string `json:"status"`
}

// FeedsListResponse Feeds列表响应
type FeedsListResponse struct {
	Feeds []xiaohongshu.Feed `json:"feeds"`
	Count int                `json:"count"`
}

// UserProfileResponse 用户主页响应
type UserProfileResponse struct {
	UserBasicInfo xiaohongshu.UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []xiaohongshu.UserInteractions `json:"interactions"`
	Feeds         []xiaohongshu.Feed             `json:"feeds"`
}

// DeleteCookies 删除 cookies 文件，用于登录重置
func (s *XiaohongshuService) DeleteCookies(ctx context.Context) error {
	cookiePath := cookies.GetCookiesFilePath()
	cookieLoader := cookies.NewLoadCookie(cookiePath)
	return cookieLoader.DeleteCookies()
}

// CheckLoginStatus 检查登录状态
func (s *XiaohongshuService) CheckLoginStatus(ctx context.Context) (*LoginStatusResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	loginAction := xiaohongshu.NewLogin(page)

	isLoggedIn, err := loginAction.CheckLoginStatus(ctx)
	if err != nil {
		return nil, err
	}

	response := &LoginStatusResponse{
		IsLoggedIn: isLoggedIn,
	}

	// 已登录时从当前页读取真实账号信息；读不到只记 warn，不影响状态返回。
	if isLoggedIn {
		if user, err := loginAction.CurrentUser(ctx); err != nil {
			logrus.Warnf("failed to get current user info: %v", err)
		} else {
			response.Username = user.Nickname
			response.UserID = user.UserID
		}
	}

	return response, nil
}

// GetLoginQrcode 获取登录的扫码二维码
func (s *XiaohongshuService) GetLoginQrcode(ctx context.Context) (*LoginQrcodeResponse, error) {
	b := newBrowser()
	page := b.NewPage()

	deferFunc := func() {
		_ = page.Close()
		b.Close()
	}

	loginAction := xiaohongshu.NewLogin(page)

	img, loggedIn, err := loginAction.FetchQrcodeImage(ctx)
	if err != nil || loggedIn {
		defer deferFunc()
	}
	if err != nil {
		return nil, err
	}

	timeout := 4 * time.Minute

	if !loggedIn {
		s.waitScanInBackground(loginAction, page, deferFunc, timeout)
	}

	return &LoginQrcodeResponse{
		Timeout: func() string {
			if loggedIn {
				return "0s"
			}
			return timeout.String()
		}(),
		Img:        img,
		IsLoggedIn: loggedIn,
	}, nil
}

// waitScanInBackground 在后台等用户扫码，扫上了就存 cookie。
//
// 浏览器必须一直活着才检测得到扫码，所以这里不能提前关；但也不能任由它堆积——
// 再取一次二维码就会把上一个还在等的会话关掉，同一时刻只留一个。
func (s *XiaohongshuService) waitScanInBackground(
	loginAction *xiaohongshu.LoginAction, page *rod.Page, closeBrowser func(), timeout time.Duration,
) {
	ctxTimeout, cancel := context.WithTimeout(context.Background(), timeout)
	seq := s.logins.start(cancel)
	logrus.Infof("等待扫码登录，会话 #%d，超时 %s", seq, timeout)

	go func() {
		defer closeBrowser()
		defer cancel()
		defer s.logins.finish(seq)

		if loginAction.WaitForLogin(ctxTimeout) {
			if err := saveCookies(page); err != nil {
				logrus.Errorf("扫码成功但保存 cookies 失败，会话 #%d: %v", seq, err)
				return
			}
			logrus.Infof("扫码登录成功，cookies 已保存，会话 #%d", seq)
			return
		}

		// 没等到扫码：要么超时，要么被新取的二维码取代
		logrus.Infof("登录会话 #%d 结束，未检测到扫码（超时或已被新的二维码取代）", seq)
	}()
}

// PublishContent 发布内容
func (s *XiaohongshuService) PublishContent(ctx context.Context, req *PublishRequest) (response *PublishResponse, err error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, ErrPublishIdentityRequired
	}

	fingerprint, err := publishRequestFingerprint(req)
	if err != nil {
		return nil, err
	}
	owner, previousResponse, previousErr := s.beginPublishAttempt(req.IdempotencyKey, fingerprint)
	if !owner {
		return previousResponse, previousErr
	}
	defer func() {
		s.finishPublishAttempt(req.IdempotencyKey, response, err)
	}()

	// 验证标题长度（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	processImage := s.processImage
	if processImage == nil {
		processImage = s.processImages
	}
	imagePaths, err := processImage(req.Images)
	if err != nil {
		return nil, err
	}

	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	content := xiaohongshu.PublishImageContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		ImagePaths:   imagePaths,
		ScheduleTime: scheduleTime,
		IsOriginal:   req.IsOriginal,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	publishImage := s.publishImage
	if publishImage == nil {
		publishImage = s.publishContent
	}
	result, err := publishImage(ctx, content)
	if err != nil {
		logrus.Errorf("发布内容失败: title=%s %v", content.Title, err)
		if errors.Is(err, xiaohongshu.ErrPublishResultUnverifiable) {
			return unknownPublishResponse(req), err
		}
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.FeedID) == "" {
		return unknownPublishResponse(req), xiaohongshu.ErrPublishResultUnverifiable
	}

	status := "VERIFIED"
	if scheduleTime != nil {
		status = "SCHEDULED"
	}
	response = &PublishResponse{
		RequestID:    req.RequestID,
		FeedID:       result.FeedID,
		XsecToken:    result.XsecToken,
		EvidenceURL:  result.EvidenceURL,
		Title:        req.Title,
		Content:      req.Content,
		Images:       len(imagePaths),
		Status:       status,
		ScheduledFor: req.ScheduleAt,
	}

	return response, nil
}

func unknownPublishResponse(req *PublishRequest) *PublishResponse {
	return &PublishResponse{
		RequestID:    req.RequestID,
		Status:       "UNKNOWN",
		ScheduledFor: req.ScheduleAt,
	}
}

func publishRequestFingerprint(req *PublishRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("生成发布请求指纹失败: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func clonePublishResponse(response *PublishResponse) *PublishResponse {
	if response == nil {
		return nil
	}
	cloned := *response
	return &cloned
}

func (s *XiaohongshuService) beginPublishAttempt(idempotencyKey, fingerprint string) (bool, *PublishResponse, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	if s.publishAttempts == nil {
		s.publishAttempts = make(map[string]*publishAttempt)
	}

	key := strings.TrimSpace(idempotencyKey)
	if previous, ok := s.publishAttempts[key]; ok {
		if previous.fingerprint != fingerprint {
			return false, nil, ErrPublishIdentityConflict
		}
		if previous.running {
			return false, nil, ErrPublishAttemptInFlight
		}
		return false, clonePublishResponse(previous.response), previous.err
	}
	s.publishAttempts[key] = &publishAttempt{fingerprint: fingerprint, running: true}
	return true, nil, nil
}

func (s *XiaohongshuService) finishPublishAttempt(idempotencyKey string, response *PublishResponse, err error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()
	attempt := s.publishAttempts[strings.TrimSpace(idempotencyKey)]
	if attempt == nil {
		return
	}
	attempt.running = false
	attempt.response = clonePublishResponse(response)
	attempt.err = err
}

// processImages 处理图片列表，支持URL下载和本地路径
func (s *XiaohongshuService) processImages(images []string) ([]string, error) {
	processor := downloader.NewImageProcessor()
	return processor.ProcessImages(images)
}

// publishContent 执行内容发布
func (s *XiaohongshuService) publishContent(ctx context.Context, content xiaohongshu.PublishImageContent) (*xiaohongshu.PublishResult, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishImageAction(page)
	if err != nil {
		return nil, err
	}

	return action.PublishWithResult(ctx, content)
}

// PublishVideo 发布视频（本地文件）
func (s *XiaohongshuService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	// 标题长度校验（小红书限制：最大20个字）
	if xhsutil.CalcTitleLength(req.Title) > 20 {
		return nil, fmt.Errorf("标题长度超过限制")
	}

	// 本地视频文件校验
	if req.Video == "" {
		return nil, fmt.Errorf("必须提供本地视频文件")
	}
	if _, err := os.Stat(req.Video); err != nil {
		return nil, fmt.Errorf("视频文件不存在或不可访问: %v", err)
	}

	var scheduleTime *time.Time
	if req.ScheduleAt != "" {
		t, err := time.Parse(time.RFC3339, req.ScheduleAt)
		if err != nil {
			return nil, fmt.Errorf("定时发布时间格式错误，请使用 ISO8601 格式: %v", err)
		}

		// 校验定时发布时间范围：1小时至14天
		now := time.Now()
		minTime := now.Add(1 * time.Hour)
		maxTime := now.Add(14 * 24 * time.Hour)

		if t.Before(minTime) {
			return nil, fmt.Errorf("定时发布时间必须至少在1小时后，当前设置: %s，最早可选: %s",
				t.Format("2006-01-02 15:04"), minTime.Format("2006-01-02 15:04"))
		}
		if t.After(maxTime) {
			return nil, fmt.Errorf("定时发布时间不能超过14天，当前设置: %s，最晚可选: %s",
				t.Format("2006-01-02 15:04"), maxTime.Format("2006-01-02 15:04"))
		}

		scheduleTime = &t
		logrus.Infof("设置定时发布时间: %s", t.Format("2006-01-02 15:04"))
	}

	content := xiaohongshu.PublishVideoContent{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		VideoPath:    req.Video,
		ScheduleTime: scheduleTime,
		Visibility:   req.Visibility,
		Products:     req.Products,
	}

	if err := s.publishVideo(ctx, content); err != nil {
		return nil, err
	}

	resp := &PublishVideoResponse{
		Title:   req.Title,
		Content: req.Content,
		Video:   req.Video,
		Status:  "发布完成",
	}
	return resp, nil
}

// publishVideo 执行视频发布
func (s *XiaohongshuService) publishVideo(ctx context.Context, content xiaohongshu.PublishVideoContent) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := xiaohongshu.NewPublishVideoAction(page)
	if err != nil {
		return err
	}

	return action.PublishVideo(ctx, content)
}

// ListFeeds 获取Feeds列表
func (s *XiaohongshuService) ListFeeds(ctx context.Context) (*FeedsListResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFeedsListAction(page)

	feeds, err := action.GetFeedsList(ctx)
	if err != nil {
		logrus.Errorf("获取 Feeds 列表失败: %v", err)
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

func (s *XiaohongshuService) SearchFeeds(ctx context.Context, keyword string, filters ...xiaohongshu.FilterOption) (*FeedsListResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewSearchAction(page)

	feeds, err := action.Search(ctx, keyword, filters...)
	if err != nil {
		return nil, err
	}

	response := &FeedsListResponse{
		Feeds: feeds,
		Count: len(feeds),
	}

	return response, nil
}

// GetFeedDetail 获取Feed详情
func (s *XiaohongshuService) GetFeedDetail(ctx context.Context, feedID, xsecToken string, loadAllComments bool) (*FeedDetailResponse, error) {
	return s.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, xiaohongshu.DefaultCommentLoadConfig())
}

// GetFeedDetailWithConfig 使用配置获取Feed详情
func (s *XiaohongshuService) GetFeedDetailWithConfig(ctx context.Context, feedID, xsecToken string, loadAllComments bool, config xiaohongshu.CommentLoadConfig) (*FeedDetailResponse, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFeedDetailAction(page)

	result, err := action.GetFeedDetailWithConfig(ctx, feedID, xsecToken, loadAllComments, config)
	if err != nil {
		return nil, err
	}

	response := &FeedDetailResponse{
		FeedID: feedID,
		Data:   result,
	}

	return response, nil
}

// UserProfile 获取用户信息
func (s *XiaohongshuService) UserProfile(ctx context.Context, userID, xsecToken, tab string) (*UserProfileResponse, error) {
	parsed, err := xiaohongshu.ParseProfileTab(tab)
	if err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewUserProfileAction(page)

	result, err := action.UserProfile(ctx, userID, xsecToken, parsed)
	if err != nil {
		return nil, err
	}
	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil

}

// PostCommentToFeed 发表评论到Feed
func (s *XiaohongshuService) PostCommentToFeed(ctx context.Context, feedID, xsecToken, content string) (*PostCommentResponse, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.PostComment(ctx, feedID, xsecToken, content); err != nil {
		return nil, err
	}

	return &PostCommentResponse{FeedID: feedID, Success: true, Message: "评论发表成功"}, nil
}

// LikeFeed 点赞笔记
func (s *XiaohongshuService) LikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Like(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "点赞成功或已点赞"}, nil
}

// UnlikeFeed 取消点赞笔记
func (s *XiaohongshuService) UnlikeFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewLikeAction(page)
	if err := action.Unlike(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消点赞成功或未点赞"}, nil
}

// FavoriteFeed 收藏笔记
func (s *XiaohongshuService) FavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Favorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "收藏成功或已收藏"}, nil
}

// UnfavoriteFeed 取消收藏笔记
func (s *XiaohongshuService) UnfavoriteFeed(ctx context.Context, feedID, xsecToken string) (*ActionResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewFavoriteAction(page)
	if err := action.Unfavorite(ctx, feedID, xsecToken); err != nil {
		return nil, err
	}
	return &ActionResult{FeedID: feedID, Success: true, Message: "取消收藏成功或未收藏"}, nil
}

// ReplyCommentToFeed 回复指定评论
func (s *XiaohongshuService) ReplyCommentToFeed(ctx context.Context, feedID, xsecToken, commentID, userID, content string) (*ReplyCommentResponse, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action := xiaohongshu.NewCommentFeedAction(page)

	if err := action.ReplyToComment(ctx, feedID, xsecToken, commentID, userID, content); err != nil {
		return nil, err
	}

	return &ReplyCommentResponse{
		FeedID:          feedID,
		TargetCommentID: commentID,
		TargetUserID:    userID,
		Success:         true,
		Message:         "评论回复成功",
	}, nil
}

// GetUnreadCount 获取通知未读数
func (s *XiaohongshuService) GetUnreadCount(ctx context.Context) (*xiaohongshu.NotificationCount, error) {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).UnreadCount(ctx)
}

// ListNotifications 获取指定分区的通知列表
func (s *XiaohongshuService) ListNotifications(ctx context.Context, tab string, limit int) (*xiaohongshu.NotificationList, error) {
	parsed, err := xiaohongshu.ParseNotificationTab(tab)
	if err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).List(ctx, parsed, limit)
}

// LikeNotification 给通知里的评论点赞或取消点赞
func (s *XiaohongshuService) LikeNotification(ctx context.Context, commentID string, unlike bool) (*xiaohongshu.NotificationLikeResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).Like(ctx, commentID, unlike)
}

// ReplyNotification 在通知页就地回复评论
func (s *XiaohongshuService) ReplyNotification(ctx context.Context, commentID, content string) (*xiaohongshu.NotificationReplyResult, error) {
	if err := requireExternalActionsEnabled(); err != nil {
		return nil, err
	}

	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return xiaohongshu.NewNotificationAction(page).Reply(ctx, commentID, content)
}

func newBrowser() *headless_browser.Browser {
	return browser.NewBrowser(configs.IsHeadless(),
		browser.WithFingerprintSeed(configs.FingerprintSeed()),
		browser.WithProxy(configs.Proxy()),
	)
}

func saveCookies(page *rod.Page) error {
	cks, err := page.Browser().GetCookies()
	if err != nil {
		return err
	}

	data, err := json.Marshal(cks)
	if err != nil {
		return err
	}

	cookieLoader := cookies.NewLoadCookie(cookies.GetCookiesFilePath())
	return cookieLoader.SaveCookies(data)
}

// withBrowserPage 执行需要浏览器页面的操作的通用函数
func withBrowserPage(fn func(*rod.Page) error) error {
	b := newBrowser()
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	return fn(page)
}

// GetMyProfile 获取当前登录用户的个人信息
func (s *XiaohongshuService) GetMyProfile(ctx context.Context, tab string) (*UserProfileResponse, error) {
	parsed, err := xiaohongshu.ParseProfileTab(tab)
	if err != nil {
		return nil, err
	}

	var result *xiaohongshu.UserProfileResponse

	err = withBrowserPage(func(page *rod.Page) error {
		action := xiaohongshu.NewUserProfileAction(page)
		result, err = action.GetMyProfileViaSidebar(ctx, parsed)
		return err
	})

	if err != nil {
		return nil, err
	}

	response := &UserProfileResponse{
		UserBasicInfo: result.UserBasicInfo,
		Interactions:  result.Interactions,
		Feeds:         result.Feeds,
	}

	return response, nil
}
