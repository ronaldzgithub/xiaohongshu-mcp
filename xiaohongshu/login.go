package xiaohongshu

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// Account readiness does not require every image/analytics resource to finish.
	// Keep one bounded context for navigation and typed account-state readback.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pp := a.page.Context(ctx)
	return checkLoginStatus(ctx,
		func() error { return pp.Navigate("https://www.xiaohongshu.com/explore") },
		func() (loginState, error) { return readLoginState(pp) },
	)
}

type loginState struct {
	Status   string `json:"status"`
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
}

// The SPA may have usable account state while window.onload still waits on
// unrelated resources. A missing state is unknown, never proof of logout.
const loginStateJS = `() => {
	const unwrap = x => x && typeof x === 'object' && x.__v_isRef === true
		? (x.value === undefined ? x._value : x.value) : x;
	const user = unwrap(window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user);
	if (!user || typeof user !== 'object') return JSON.stringify({status:'pending'});
	const info = unwrap(user.userInfo) || user;
	const loggedIn = unwrap(user.loggedIn);
	if (info.guest === true || (loggedIn === false && document.querySelector('.login-container')))
		return JSON.stringify({status:'unauthenticated'});
	const userId = info.userId || info.user_id;
	if (loggedIn !== false && typeof userId === 'string' && userId.trim())
		return JSON.stringify({status:'authenticated',userId:userId,nickname:info.nickname||''});
	return JSON.stringify({status:'pending'});
}`

func readLoginState(page *rod.Page) (loginState, error) {
	res, err := page.Eval(loginStateJS)
	if err != nil {
		return loginState{}, errors.Wrap(err, "read typed login state failed")
	}
	var state loginState
	if err := json.Unmarshal([]byte(res.Value.String()), &state); err != nil {
		return loginState{}, errors.Wrap(err, "decode typed login state failed")
	}
	return state, nil
}

func checkLoginStatus(ctx context.Context, navigate func() error, observe func() (loginState, error)) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, errors.Wrap(err, "login status cancelled")
	}
	if err := navigate(); err != nil {
		return false, errors.Wrap(err, "navigate login status failed")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return false, errors.Wrap(err, "typed login state not ready")
		}
		state, err := observe()
		if err != nil {
			return false, errors.Wrap(err, "check login status failed")
		}
		if err := ctx.Err(); err != nil {
			return false, errors.Wrap(err, "typed login state not ready")
		}
		switch state.Status {
		case "authenticated":
			if strings.TrimSpace(state.UserID) == "" {
				return false, errors.New("typed login state is missing account subject")
			}
			return true, nil
		case "unauthenticated":
			return false, nil
		case "pending":
		default:
			return false, errors.New("typed login state is invalid")
		}
		select {
		case <-ctx.Done():
			return false, errors.Wrap(ctx.Err(), "typed login state not ready")
		case <-ticker.C:
		}
	}
}

// CurrentUser 当前登录用户的基础信息。
type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息。
// 需在 CheckLoginStatus 之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	pp := a.page.Context(ctx).Timeout(10 * time.Second)

	state, err := readLoginState(pp)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}

	if state.Status != "authenticated" || strings.TrimSpace(state.UserID) == "" {
		return nil, errors.New("current user not found in page state")
	}
	return &CurrentUser{Nickname: state.Nickname, UserID: state.UserID}, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return nil
	}

	pp.MustElement(".main-container .user .link-wrapper .channel")

	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(2 * time.Second)

	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return "", true, nil
	}

	src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			el, err := pp.Element(".main-container .user .link-wrapper .channel")
			if err == nil && el != nil {
				return true
			}
		}
	}
}
