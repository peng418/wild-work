// Package app 业务编排层：HTTP 管理 API + 托盘动作 + 登录编排 + 日志。
// 由 daemon 入口（cmd/wild-work）装配，替代旧版 wails 绑定层。
package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wild-work/internal/auth"
	"wild-work/internal/config"
	"wild-work/internal/login"
	loginqoder "wild-work/internal/login_qoder"
	loginqodercn "wild-work/internal/login_qodercn"
	loginqodercom "wild-work/internal/login_qodercom"
	loginqwenwork "wild-work/internal/login_qwenwork"
	logintrae "wild-work/internal/login_trae"
	"wild-work/internal/login_wbai"
	"wild-work/internal/platform"
	"wild-work/internal/pool"
	"wild-work/internal/provider"
	"wild-work/internal/qoder"
	"wild-work/internal/qodercn"
	"wild-work/internal/qodercom"
	"wild-work/internal/qwenwork"
	"wild-work/internal/scheduler"
	"wild-work/internal/server"
)

// Version 版本号。
const Version = "2.4.1"

const (
	loginTimeout   = 5 * time.Minute
	loginPollEvery = 2 * time.Second
)

// Runtime 一个渠道的运行时资源。
type Runtime struct {
	Kind      provider.Kind
	Pool      *pool.Pool
	Upstream  provider.Upstream
	Scheduler *scheduler.Scheduler
}

// Options 构建 App 的依赖。
type Options struct {
	ConfigPath string
	Config     *config.Config
	Runtimes   map[provider.Kind]*Runtime
	Handler    *server.Handler
}

// App 应用编排。
type App struct {
	cfgPath  string
	cfg      *config.Config
	runtimes map[provider.Kind]*Runtime
	handler  *server.Handler // 内层（保留强类型，用于 SetAPIKey/ChannelModels 等）
	httpRoot http.Handler    // 对外暴露的根 handler，可能为外层兼容层 mux（见 SetRootHandler）

	mu         sync.Mutex // 保护 httpSrv / cfg 修改
	httpSrv    *http.Server
	listenAddr string // 当前监听地址；同地址重复 serve 直接复用，避免自撞端口

	muLogin      sync.Mutex
	loginBusy    bool
	loginCtx     context.Context
	loginCancel  context.CancelFunc
	loginClient  *http.Client
	loginStateFP string
	loginKind    provider.Kind
	pricingFP    string

	logFile *os.File

	// compatSyncer 面板保存 compat 后同步给外层兼容层（热更新路由表）。
	// 由 main.go 注入；nil 时仅写配置不热更（下次启动生效）。
	compatSyncer func(defaultChannel string, maxTokensCap int, modelMap map[string]string)

	refreshMu  sync.Mutex // 防并发刷新积分
	refreshing bool

	pricingMu      sync.Mutex
	pricingCache   []provider.ModelPricing // 本地缓存
	pricingFetched time.Time
	pricingErr     string // 最近一次拉取错误
	pricingBusy    bool   // 防并发刷新（启动刷新与面板触发可能重叠）
}

// New 构建 App 并接管全局日志（写文件 + 环形缓冲）。
func New(opts Options) (*App, error) {
	a := &App{
		cfgPath:  opts.ConfigPath,
		cfg:      opts.Config,
		runtimes: opts.Runtimes,
		handler:  opts.Handler,
	}
	a.loginStateFP = filepath.Join(filepath.Dir(opts.Config.StateFile), "login-state.json")
	a.pricingFP = filepath.Join(filepath.Dir(opts.Config.StateFile), "pricing-cache.json")

	// 日志文件 data/app.log
	logFP := filepath.Join(filepath.Dir(opts.Config.StateFile), "app.log")
	if err := os.MkdirAll(filepath.Dir(logFP), 0o755); err == nil {
		f, err := os.OpenFile(logFP, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			a.logFile = f
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(&logWriter{app: a})
	a.loadPricingCache()
	return a, nil
}

// Close 关闭日志文件。
func (a *App) Close() {
	if a.logFile != nil {
		_ = a.logFile.Close()
		a.logFile = nil
	}
}

func (a *App) runtime(kind provider.Kind) *Runtime {
	if a.runtimes == nil {
		return nil
	}
	return a.runtimes[kind]
}

func (a *App) firstRuntime() *Runtime {
	for _, k := range []provider.Kind{provider.WorkBuddy, provider.WorkBuddyAI, provider.TraeWork, provider.Qoder, provider.QoderCN, provider.QoderCOM, provider.QwenWork} {
		if rt := a.runtime(k); rt != nil {
			return rt
		}
	}
	return nil
}

func (a *App) totalAccounts() int {
	n := 0
	for _, rt := range a.runtimes {
		if rt != nil && rt.Pool != nil {
			n += len(rt.Pool.List())
		}
	}
	return n
}

func (a *App) allStatuses() []pool.Status {
	out := []pool.Status{}
	for _, rt := range a.runtimes {
		if rt != nil && rt.Pool != nil {
			out = append(out, rt.Pool.List()...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

// noExplicitCheckinKinds 不支持显式签到（手动按钮）的渠道。
// WorkBuddy 国际版改为定时自动对话保活并领取日活奖励（详见 workbuddyai.DailyCheckin），
// 无需用户手动触发，故也不提供手动签到入口；
// 千问办公无签到活动且每日积分服务端被动发放，无需领取/保活。
// QoderCN 已实现双路径签到（campaigns 主路径），支持手动按钮。
func noExplicitCheckin(k provider.Kind) bool {
	return k == provider.WorkBuddyAI || k == provider.QwenWork
}

func (a *App) findRuntimeAuth(uid string) (*Runtime, *auth.Auth) {
	for _, rt := range a.runtimes {
		if rt == nil || rt.Pool == nil {
			continue
		}
		if au := rt.Pool.AuthByUID(uid); au != nil {
			return rt, au
		}
	}
	return nil, nil
}

func (a *App) checkinTimes() []string {
	if rt := a.firstRuntime(); rt != nil && rt.Scheduler != nil {
		return rt.Scheduler.CheckinTimes()
	}
	return nil
}

func (a *App) keepaliveHours() []int {
	if rt := a.firstRuntime(); rt != nil && rt.Scheduler != nil {
		return rt.Scheduler.KeepaliveHours()
	}
	return nil
}

func (a *App) nextFire() time.Time {
	if rt := a.firstRuntime(); rt != nil && rt.Scheduler != nil {
		return rt.Scheduler.NextFire()
	}
	return time.Time{}
}

// SetHandler 注入内层 HTTP handler（在 HandleAPI 注册后调用）。
func (a *App) SetHandler(h *server.Handler) {
	a.handler = h
	if a.httpRoot == nil {
		a.httpRoot = h
	}
}

// SetRootHandler 注入对外服务的根 handler（通常是「兼容层 mux + 内层 handler」的组合）。
// httpRoot 为 nil 时回退到内层 handler。
func (a *App) SetRootHandler(h http.Handler) { a.httpRoot = h }

// ---------------------------------------------------------------------------
// HTTP 服务
// ---------------------------------------------------------------------------

// serveHandler 返回实际对外服务的 handler（优先外层组合，其次内层）。
func (a *App) serveHandler() http.Handler {
	if a.httpRoot != nil {
		return a.httpRoot
	}
	if a.handler == nil { // 避免 typed-nil 接口导致 http.Server 请求时 panic
		return nil
	}
	return a.handler
}

// StartServer 按当前配置启动 HTTP 服务。
func (a *App) StartServer() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.serveLocked(a.cfg.Listen.Addr())
}

// serveLocked 在 newAddr 上启动新服务并切换；调用方需持有 a.mu。
func (a *App) serveLocked(addr string) error {
	// 地址未变时直接复用现有 listener：否则「先 Listen 再 Shutdown 旧」在同端口上
	// 会撞自身（Only one usage of each socket address），导致面板保存映射/密钥时报 400。
	if a.httpSrv != nil && a.listenAddr == addr {
		return nil
	}
	// 先停旧服务再绑新地址：避免同端口切换时新旧 listener 短暂重叠报「端口被占用」。
	if old := a.httpSrv; old != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = old.Shutdown(shutdownCtx)
		cancel()
		a.httpSrv = nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           a.serveHandler(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	a.httpSrv = srv
	a.listenAddr = addr
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http serve %s: %v", addr, err)
		}
	}()
	return nil
}

// Stop 停止 HTTP 服务（退出时调用）。
func (a *App) Stop() {
	a.mu.Lock()
	srv := a.httpSrv
	a.httpSrv = nil
	a.listenAddr = ""
	a.mu.Unlock()
	if srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
	a.Close()
}

// Quit 退出整个程序（Web UI“退出”按钮调用）。
func (a *App) Quit() {
	a.Stop()
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// 账号操作
// ---------------------------------------------------------------------------

// StartLoginFor 发起指定渠道登录：workbuddy / workbuddyai / traework / qoder / qodercn。
func (a *App) StartLoginFor(kind string) (string, error) {
	k := provider.Kind(strings.TrimSpace(kind))
	if k == "" {
		k = provider.WorkBuddy
	}
	switch k {
	case provider.WorkBuddy, provider.WorkBuddyAI, provider.TraeWork, provider.Qoder, provider.QoderCN, provider.QoderCOM, provider.QwenWork:
		// 这些渠道已有登录编排
	default:
		return "", fmt.Errorf("unknown login provider %s", kind)
	}
	a.muLogin.Lock()
	if a.loginBusy {
		a.muLogin.Unlock()
		return "", errors.New("已有登录流程进行中，请先完成或取消")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.loginCtx, a.loginCancel = ctx, cancel
	a.loginKind = k
	switch k {
	case provider.TraeWork:
		a.loginClient = logintrae.NewClient()
	case provider.Qoder:
		a.loginClient = loginqoder.NewClient()
	case provider.QoderCN:
		a.loginClient = loginqodercn.NewClient()
	case provider.QoderCOM:
		a.loginClient = loginqodercom.NewClient()
	case provider.WorkBuddyAI:
		a.loginClient = loginwbai.NewClient()
	case provider.QwenWork:
		a.loginClient = loginqwenwork.NewClient()
	default:
		a.loginClient = login.NewClient()
	}
	a.loginBusy = true
	a.muLogin.Unlock()

	var authURL string
	var err error
	switch k {
	case provider.TraeWork:
		authURL, err = logintrae.Start(a.loginClient, a.loginStateFP)
	case provider.Qoder:
		authURL, err = loginqoder.Start(a.loginClient, a.loginStateFP)
	case provider.QoderCN:
		authURL, err = loginqodercn.Start(a.loginClient, a.loginStateFP)
	case provider.QoderCOM:
		authURL, err = loginqodercom.Start(a.loginClient, a.loginStateFP)
	case provider.WorkBuddyAI:
		authURL, err = loginwbai.Start(a.loginClient, a.loginStateFP)
	case provider.QwenWork:
		authURL, err = loginqwenwork.Start(a.loginClient, a.loginStateFP)
	default:
		ep := login.EndpointsForRegion(a.cfg.Region)
		authURL, err = login.Start(a.loginClient, a.loginStateFP, ep)
		if err == nil {
			if resolved, rerr := login.ResolveAuthURL(a.loginClient, authURL, ep); rerr == nil && resolved != "" {
				authURL = resolved
			}
		}
	}
	if err != nil {
		a.finishLogin()
		return "", err
	}
	// 浏览器打开由前端 Web UI 通过 window.open() 完成
	go a.pollLogin(ctx)
	log.Printf("%s 登录流程已发起", k)
	return authURL, nil
}

// CancelLogin 取消当前登录流程。
func (a *App) CancelLogin() error {
	a.muLogin.Lock()
	cancel := a.loginCancel
	kind := a.loginKind
	a.muLogin.Unlock()
	if cancel == nil {
		return errors.New("没有进行中的登录")
	}
	cancel()
	_ = os.Remove(a.loginStateFP)
	// 千问办公登录附带本机回调 server，取消时一并关闭
	if kind == provider.QwenWork {
		loginqwenwork.Shutdown()
	}
	log.Printf("登录已取消")
	return nil
}

// pollLogin 后台轮询登录结果，写日志。
func (a *App) pollLogin(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("login poll panic: %v", r)
		}
		a.finishLogin()
	}()
	deadline := time.Now().Add(loginTimeout)
	log.Printf("登录流程已发起，等待浏览器完成…")
	t := time.NewTicker(loginPollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("登录已取消")
			return
		case <-t.C:
		}
		if time.Now().After(deadline) {
			log.Printf("登录超时，请重新发起")
			return
		}
		if a.loginKind == provider.TraeWork {
			r, err := logintrae.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeTraeLogin(r)
				return
			}
			if !errors.Is(err, logintrae.ErrPending) {
				log.Printf("trae login poll failed: %v", err)
			}
			continue
		}
		if a.loginKind == provider.Qoder {
			r, err := loginqoder.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeQoderLogin(r)
				return
			}
			if !errors.Is(err, loginqoder.ErrPending) {
				log.Printf("qoder login poll failed: %v", err)
			}
			continue
		}
		if a.loginKind == provider.QoderCN {
			r, err := loginqodercn.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeQoderCNLogin(r)
				return
			}
			if !errors.Is(err, loginqodercn.ErrPending) {
				log.Printf("qodercn login poll failed: %v", err)
			}
			continue
		}
		if a.loginKind == provider.QoderCOM {
			r, err := loginqodercom.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeQoderCOMLogin(r)
				return
			}
			if !errors.Is(err, loginqodercom.ErrPending) {
				log.Printf("qodercom login poll failed: %v", err)
			}
			continue
		}
		if a.loginKind == provider.WorkBuddyAI {
			r, err := loginwbai.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeWbaiLogin(r)
				return
			}
			if !errors.Is(err, loginwbai.ErrPending) {
				log.Printf("workbuddyai login poll failed: %v", err)
			}
			continue
		}
		if a.loginKind == provider.QwenWork {
			r, err := loginqwenwork.Poll(a.loginClient, a.loginStateFP)
			if err == nil {
				a.completeQwenWorkLogin(r)
				return
			}
			if !errors.Is(err, loginqwenwork.ErrPending) {
				log.Printf("qwenwork login poll failed: %v", err)
			}
			continue
		}
		r, err := login.Poll(a.loginClient, a.loginStateFP, login.EndpointsForRegion(a.cfg.Region))
		if err == nil {
			a.completeLogin(r)
			return
		}
		if !errors.Is(err, login.ErrPending) {
			log.Printf("workbuddy login poll failed: %v", err)
		}
	}
}

// completeLogin 登录成功：写 auth 文件、重载账号池，异步签到。
func (a *App) completeLogin(r login.Result) {
	log.Printf("workbuddy 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t", r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	fp, err := login.SaveAuth(a.cfg.AuthDir, r)
	if err != nil {
		log.Printf("workbuddy 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	log.Printf("workbuddy 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.WorkBuddy)
	a.safeGo(func() {
		rt := a.runtime(provider.WorkBuddy)
		if rt == nil || rt.Scheduler == nil {
			return
		}
		if res, err := rt.Scheduler.CheckinAccount(r.UID); err != nil {
			log.Printf("新账号签到失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("新账号签到完成 %s：%s", r.Nickname, res.Msg)
		}
		// 积分兑底：签到路径失败（余额查询失败/签到异常）时 pool 内 credits 仍为 0，
		// 独立刷一次保证新账号首屏正确（幂等：多刷无害）。
		if _, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("workbuddy 新账号积分兑底失败 %s: %v", r.Nickname, err)
		}
	})
}

// afterAccountAdded 新账号就绪后的统一收尾：刷新模型列表与费率。
// 原因：模型列表与费率都按「已接入账号」填充，新渠道/新账号加入后，
// 若不主动失效缓存，两处都要等 TTL（1h / 1h）才更新，面板上显示不完整。
// 故每个渠道登录成功后统一调用。
func (a *App) afterAccountAdded(kind provider.Kind) {
	// 1) 使动态模型缓存失效，下次 /v1/models 与面板重新拉上游
	if a.handler != nil {
		a.handler.InvalidateModels()
	}
	// 2) 立即刷新费率为后台任务（含模型列表拉取），不阻塞登录流程
	a.safeGo(func() {
		a.RefreshPricing()
		log.Printf("account added platform=%s: models+pricing refreshed", kind)
	})
}

// completeWbaiLogin 国际版登录成功：写 auth 文件、重载账号池、异步拉取初始积分。
// 不触发签到（国际版无签到活动）；但必须拉一次余额，
// 否则新账号在面板上显示 0 积分，需用户手动点「刷新积分」才正确。
func (a *App) completeWbaiLogin(r loginwbai.Result) {
	log.Printf("workbuddyai 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t",
		r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	if r.UID == "" {
		log.Printf("workbuddyai 登录失败：响应缺 uid")
		a.finishLogin()
		return
	}
	fp, err := loginwbai.SaveAuth(a.cfg.AuthDir, r)
	if err != nil {
		log.Printf("workbuddyai 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		a.finishLogin()
		return
	}
	log.Printf("workbuddyai 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.WorkBuddyAI)
	// 新账号首次拉取余额（异步，不阻塞登录流程）
	a.safeGo(func() {
		if remain, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("workbuddyai 新账号积分获取失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("workbuddyai 新账号积分获取完成 %s: %d", r.Nickname, remain)
		}
	})
}

func (a *App) completeTraeLogin(r logintrae.Result) {
	log.Printf("traework 登录成功 uid=%s nickname=%s expires_at=%d refresh_token=%t", r.UID, r.Nickname, r.ExpiresAt, r.RefreshToken != "")
	fp, err := logintrae.SaveAuth(a.cfg.AuthDir, r)
	if err != nil {
		log.Printf("traework 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	log.Printf("traework 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.TraeWork)
	a.safeGo(func() {
		rt := a.runtime(provider.TraeWork)
		if rt == nil || rt.Scheduler == nil {
			return
		}
		if res, err := rt.Scheduler.CheckinAccount(r.UID); err != nil {
			log.Printf("TraeWork 新账号签到失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("TraeWork 新账号签到完成 %s：%s", r.Nickname, res.Msg)
		}
		// 积分兑底（对齐 workbuddy）：签到路径失败时 pool 内 credits 仍为 0
		if _, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("TraeWork 新账号积分兑底失败 %s: %v", r.Nickname, err)
		}
	})
}

// completeQoderLogin 登录成功：生成机器指纹、写 auth 文件、重载账号池。
// Qoder 当前无签到活动，不做首次签到。
func (a *App) completeQoderLogin(r loginqoder.Result) {
	log.Printf("qoder 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t", r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	au := &auth.Auth{Kind: "qoder", AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, UID: r.UID, Nickname: r.Nickname}
	qoder.EnsureFingerprint(au)
	fp, err := loginqoder.SaveAuth(a.cfg.AuthDir, r, au.MachineID, au.MachineToken, au.MachineType)
	if err != nil {
		log.Printf("qoder 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	log.Printf("qoder 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.Qoder)
	// 新账号首次拉取余额（对齐 workbuddyai/qwenwork：qoder 登录后无签到流程，
	// 不主动刷则面板显示 0 直到下个 auto-refresh 周期）
	a.safeGo(func() {
		if remain, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("qoder 新账号积分获取失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("qoder 新账号积分获取完成 %s: %d", r.Nickname, remain)
		}
	})
}

// completeQoderCNLogin 登录成功：生成机器指纹、写 auth 文件、重载账号池、首次签到。
func (a *App) completeQoderCNLogin(r loginqodercn.Result) {
	log.Printf("qodercn 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t", r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	au := &auth.Auth{Kind: "qodercn", AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, UID: r.UID, Nickname: r.Nickname}
	qodercn.EnsureFingerprint(au)
	fp, err := loginqodercn.SaveAuth(a.cfg.AuthDir, r, au.MachineID, au.MachineToken, au.MachineType)
	if err != nil {
		log.Printf("qodercn 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	au.FilePath = fp // 回填落盘路径，后续 SaveAtomic（显示名回填）依赖它
	log.Printf("qodercn 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.QoderCN)
	// 新账号首次拉取余额 + 实测 userType + 回填显示名。
	// 设备流响应里无 nickname，显示名（邮箱/昵称）只能从 userinfo 拿：
	// 拉到后写回 au.Nickname 并 SaveAtomic 落盘，面板即显示真实用户名。
	a.safeGo(func() {
		if rt := a.runtime(provider.QoderCN); rt != nil {
			if cu, ok := rt.Upstream.(*qodercn.Client); ok {
				if name, _, err := cu.FetchUserInfo(au); err != nil {
					log.Printf("qodercn 新账号 userType 实测失败 %s: %v", r.UID, err)
				} else if name != "" && au.Nickname == "" {
					au.Nickname = name
					if err := au.SaveAtomic(); err != nil {
						log.Printf("qodercn 显示名回写失败 %s: %v", r.UID, err)
					} else {
						log.Printf("qodercn 显示名已回填 %s: %s", r.UID, name)
						a.reloadAccounts() // 池内 nickname 刷新，面板即时可见
					}
				}
			}
		}
		if remain, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("qodercn 新账号积分获取失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("qodercn 新账号积分获取完成 %s: %d", r.Nickname, remain)
		}
	})
}

// completeQoderCOMLogin 登录成功：生成机器指纹、写 auth 文件、重载账号池、首次签到（仅 campaigns）。
func (a *App) completeQoderCOMLogin(r loginqodercom.Result) {
	log.Printf("qodercom 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t", r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	au := &auth.Auth{Kind: "qodercom", AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, UID: r.UID, Nickname: r.Nickname}
	qodercom.EnsureFingerprint(au)
	fp, err := loginqodercom.SaveAuth(a.cfg.AuthDir, r, au.MachineID, au.MachineToken, au.MachineType)
	if err != nil {
		log.Printf("qodercom 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	au.FilePath = fp // 回填落盘路径，后续 SaveAtomic（显示名回填）依赖它
	log.Printf("qodercom 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.QoderCOM)
	// 新账号首次拉取余额 + 实测 userType + 回填显示名。
	// 设备流响应里无 nickname，显示名（邮箱/昵称）只能从 userinfo 拿：
	// 拉到后写回 au.Nickname 并 SaveAtomic 落盘，面板即显示真实用户名。
	a.safeGo(func() {
		if rt := a.runtime(provider.QoderCOM); rt != nil {
			if cu, ok := rt.Upstream.(*qodercom.Client); ok {
				if name, _, err := cu.FetchUserInfo(au); err != nil {
					log.Printf("qodercom 新账号 userType 实测失败 %s: %v", r.UID, err)
				} else if name != "" && au.Nickname == "" {
					au.Nickname = name
					if err := au.SaveAtomic(); err != nil {
						log.Printf("qodercom 显示名回写失败 %s: %v", r.UID, err)
					} else {
						log.Printf("qodercom 显示名已回填 %s: %s", r.UID, name)
						a.reloadAccounts() // 池内 nickname 刷新，面板即时可见
					}
				}
			}
		}
		if remain, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("qodercom 新账号积分获取失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("qodercom 新账号积分获取完成 %s: %d", r.Nickname, remain)
		}
	})
}

// completeQwenWorkLogin 登录成功：写 auth 文件、重载账号池。
// 千问办公无签到活动（每日积分服务端被动发放），不做首次签到。
func (a *App) completeQwenWorkLogin(r loginqwenwork.Result) {
	log.Printf("qwenwork 登录成功 uid=%s nickname=%s expires_in=%d refresh_token=%t",
		r.UID, r.Nickname, r.ExpiresIn, r.RefreshToken != "")
	fp, err := loginqwenwork.SaveAuth(a.cfg.AuthDir, r)
	if err != nil {
		log.Printf("qwenwork 登录保存凭证失败 uid=%s err=%v", r.UID, err)
		return
	}
	log.Printf("qwenwork 登录凭证已保存 uid=%s file=%s", r.UID, filepath.Base(fp))
	a.reloadAccounts()
	a.finishLogin()
	a.afterAccountAdded(provider.QwenWork)
	// 首屏初始化（顺序敏感，串行执行）：
	// 1) 刷新余额 —— 若 Poll 兑换的首个 token 无效（实测 OAuth 兑换 token 调 /user/info
	//    会 401 invalid-credential），refreshIfSessionDead 会自动换新 token；
	// 2) 之后再用有效 token 拉昵称兑底（Poll 兑换的 JWT 实测无 username 字段，
	//    refresh 后的 device_token 才有），写回 auth 文件，否则面板显示 hex uid。
	a.safeGo(func() {
		var nickname = r.Nickname
		if remain, err := a.RefreshCredits(r.UID); err != nil {
			log.Printf("qwenwork 新账号积分获取失败 %s: %v", r.Nickname, err)
		} else {
			log.Printf("qwenwork 新账号积分获取完成 %s: %d", r.Nickname, remain)
		}
		if nickname == "" {
			rt := a.runtime(provider.QwenWork)
			if rt == nil || rt.Upstream == nil {
				return
			}
			au := rt.Pool.AuthByUID(r.UID)
			if au == nil {
				return
			}
			qw, ok := rt.Upstream.(*qwenwork.Client)
			if !ok {
				return
			}
			if nick, nerr := qw.FetchNickname(au); nerr == nil && nick != "" {
				nickname = nick
				au.Nickname = nick // AuthByUID 返回池内指针，改字段即生效；SaveAtomic 自带锁
				_ = au.SaveAtomic()
				log.Printf("qwenwork 昵称兑底成功 uid=%s nickname=%s", r.UID, nick)
			} else if nerr != nil {
				log.Printf("qwenwork 昵称兑底失败 uid=%s err=%v", r.UID, nerr)
			}
		}
	})
}

// mustLoadQwenWork 重新扫描千问办公凭证目录（错误仅记日志）。
func mustLoadQwenWork(dir string) []*auth.Auth {
	auths, err := auth.LoadQwenWorkDir(dir)
	if err != nil {
		log.Printf("reload qwenwork accounts: %v", err)
		return nil
	}
	return auths
}

func (a *App) finishLogin() {
	a.muLogin.Lock()
	a.loginBusy = false
	a.loginCtx, a.loginCancel, a.loginClient = nil, nil, nil
	a.loginKind = ""
	a.muLogin.Unlock()
}

// reloadAccounts 用 auths 目录最新文件对齐账号池。
func (a *App) reloadAccounts() {
	if rt := a.runtime(provider.WorkBuddy); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadWorkBuddyDir(a.cfg.AuthDir, a.cfg.Region)
		if err != nil {
			log.Printf("reload workbuddy accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.TraeWork); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadTraeDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload traework accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.Qoder); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadQoderDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload qoder accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.QoderCN); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadQoderCNDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload qodercn accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.QoderCOM); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadQoderCOMDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload qodercom accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.WorkBuddyAI); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadWorkBuddyAiDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload workbuddyai accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
	if rt := a.runtime(provider.QwenWork); rt != nil && rt.Pool != nil {
		auths, err := auth.LoadQwenWorkDir(a.cfg.AuthDir)
		if err != nil {
			log.Printf("reload qwenwork accounts: %v", err)
		} else {
			rt.Pool.SyncToDir(auths)
		}
	}
}

// CheckinAccount 单个账号立即签到。
func (a *App) CheckinAccount(uid string) (scheduler.CheckinResult, error) {
	rt, _ := a.findRuntimeAuth(uid)
	if rt == nil || rt.Scheduler == nil {
		return scheduler.CheckinResult{}, fmt.Errorf("unknown account %s", uid)
	}
	if noExplicitCheckin(rt.Kind) {
		if rt.Kind == provider.WorkBuddyAI {
			return scheduler.CheckinResult{}, fmt.Errorf("workbuddyai 渠道无需手动签到，已定时自动领取日活奖励")
		}
		return scheduler.CheckinResult{}, fmt.Errorf("%s 渠道不支持手动签到", rt.Kind)
	}
	res, err := rt.Scheduler.CheckinAccount(uid)
	if err != nil {
		log.Printf("checkin failed uid=%s err=%v", uid, err)
		return res, err
	}
	log.Printf("checkin uid=%s ok=%t msg=%s remain=%d has_remain=%t", uid, res.OK, res.Msg, res.Remain, res.HasRemain)
	return res, nil
}

// CheckinAll 全部账号立即签到。
func (a *App) CheckinAll() []scheduler.CheckinResult {
	results := make([]scheduler.CheckinResult, 0)
	for _, rt := range a.runtimes {
		if rt == nil || rt.Pool == nil || rt.Scheduler == nil {
			continue
		}
		if noExplicitCheckin(rt.Kind) { // 无签到活动渠道，跳过
			continue
		}
		for _, st := range rt.Pool.List() {
			if st.Disabled {
				results = append(results, scheduler.CheckinResult{UID: st.UID, Msg: "账号已禁用"})
				continue
			}
			res, err := rt.Scheduler.CheckinAccount(st.UID)
			if err != nil {
				res = scheduler.CheckinResult{UID: st.UID, Msg: err.Error()}
			}
			results = append(results, res)
		}
	}
	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}
	log.Printf("批量签到完成：total=%d ok=%d failed=%d", len(results), ok, len(results)-ok)
	return results
}

// CreditRefreshInterval 积分自动刷新间隔。
// 无签到活动的渠道（如 WorkBuddy 国际版）积分不随签到更新，
// 若不定期刷新：面板长期显示旧值（新账号则一直为 0），
// 且 Pool.Pick() 按积分排序会因此长期选错账号。
const CreditRefreshInterval = 30 * time.Minute

// refreshIfSessionDead 上游报「登录态失效」时刷新一次 token 并写回，返回是否已刷新。
//
// 为何不能只依赖 NeedsRefresh：它比的是本地 expiresAt，而本地时钟可能是错的——
// 例如 token 已在别处（另一实例/客户端）被轮换、或上次刷新后未落盘，
// 此时本地仍显示「有效」而上游已拒绝 → 不刷新就永久卡在 401（积分恒为 0、明细恒为空）。
// 故必须对 401 本身做一次「刷新 + 重试」。
func (a *App) refreshIfSessionDead(rt *Runtime, au *auth.Auth, err error) bool {
	var ue *provider.Error
	if !errors.As(err, &ue) || ue.Kind != provider.ErrSessionDead {
		return false
	}
	if au.RefreshToken == "" {
		return false
	}
	log.Printf("session dead, refreshing platform=%s uid=%s", rt.Kind, au.UID)
	if rerr := rt.Upstream.RefreshToken(au); rerr != nil {
		log.Printf("session dead refresh failed platform=%s uid=%s err=%v", rt.Kind, au.UID, rerr)
		return false
	}
	// 刷新成功必须落盘：否则下次启动又拿旧 token，重回 401。
	if serr := au.SaveAtomic(); serr != nil {
		log.Printf("session dead refresh save failed platform=%s uid=%s err=%v", rt.Kind, au.UID, serr)
	}
	return true
}

// creditTotals 一次上游调用同时取回「可消耗余额」「临期额度」与「不可消耗余额」。
// 三者同源于 UserResourceDetail 的单次响应：remain 即 pool 路由口径的可消耗余额，
// 临期额度为其中 24h 内（到期日≤明天）到期的部分，不可消耗部分由条目的 Usable 标记汇总得到
//（渠道不区分专用池时为 0；渠道不下发到期时间时临期为 0，如 Qoder）。
// 遇 401 自动刷新 token 并重试一次（见 refreshIfSessionDead）。
func (a *App) creditTotals(rt *Runtime, au *auth.Auth) (usable, expiring, unusable int64, err error) {
	remain, items, err := rt.Upstream.UserResourceDetail(au)
	if err != nil && a.refreshIfSessionDead(rt, au, err) {
		remain, items, err = rt.Upstream.UserResourceDetail(au)
	}
	if err != nil {
		return 0, 0, 0, err
	}
	_, unusable = provider.Summarize(items)
	expiring = provider.ExpiringWithin(items, 24*time.Hour)
	return remain, expiring, unusable, nil
}

// StartCreditAutoRefresh 后台定期刷新指定渠道的账号积分。
// 启动立即刷一次，之后每隔 interval 一次；单账号失败不影响其他账号。
//
// 范围限制在传入的 kinds：国内版 workbuddy 有自己的签到刷新途径，
// 不纳入此循环，以免改变其行为。
func (a *App) StartCreditAutoRefresh(ctx context.Context, kinds []provider.Kind, interval time.Duration) {
	if interval <= 0 {
		interval = CreditRefreshInterval
	}
	go func() {
		refresh := func() {
			for _, k := range kinds {
				rt := a.runtime(k)
				if rt == nil || rt.Pool == nil || rt.Upstream == nil {
					continue
				}
				for _, st := range rt.Pool.List() {
					if st.Disabled {
						continue
					}
					au := rt.Pool.AuthByUID(st.UID)
					if au == nil {
						continue
					}
					// token 临近过期时先刷新（国际版 token 有效期长，通常不触发）
					if au.NeedsRefresh(10 * time.Minute) {
						if err := rt.Upstream.RefreshToken(au); err != nil {
							log.Printf("credit auto-refresh token refresh failed platform=%s uid=%s err=%v", k, st.UID, err)
							continue
						}
						if err := au.SaveAtomic(); err != nil {
							log.Printf("credit auto-refresh token save failed platform=%s uid=%s err=%v", k, st.UID, err)
						}
					}
					usable, expiring, unusable, err := a.creditTotals(rt, au)
					if err != nil {
						log.Printf("credit auto-refresh failed platform=%s uid=%s err=%v", k, st.UID, err)
						continue
					}
					rt.Pool.SetCreditDetail(st.UID, usable, expiring, unusable)
					log.Printf("credit auto-refresh platform=%s uid=%s remain=%d expiring=%d unusable=%d", k, st.UID, usable, expiring, unusable)
				}
			}
		}
		refresh() // 启动即刷：新账号不必等下一个周期
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
}

// PricingRefreshInterval 模型列表与费率的后台自动刷新间隔。
// 两者都随上游变动，且面板依赖它们展示，故定期刷新保持新鲜。
const PricingRefreshInterval = 30 * time.Minute

// StartPricingAutoRefresh 后台定期刷新「模型列表 + 费率」。
// 启动立即刷一次：否则刚启动时费率缓存为空，面板下方全是 unknown，观感不佳。
// 之后每隔 interval 一次；取消 ctx 即停止。
func (a *App) StartPricingAutoRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = PricingRefreshInterval
	}
	go func() {
		// 启动刷新：同时失效动态模型缓存，保证模型列表也是新的
		if a.handler != nil {
			a.handler.InvalidateModels()
		}
		a.RefreshPricing()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.RefreshPricing()
			}
		}
	}()
}

// RefreshCredits 刷新单个账号积分（返回可消耗余额）。
func (a *App) RefreshCredits(uid string) (int64, error) {
	rt, au := a.findRuntimeAuth(uid)
	if rt == nil || au == nil {
		return 0, fmt.Errorf("unknown account %s", uid)
	}
	log.Printf("credits refresh start platform=%s uid=%s", rt.Kind, uid)
	usable, expiring, unusable, err := a.creditTotals(rt, au)
	if err != nil {
		log.Printf("credits refresh failed platform=%s uid=%s err=%v", rt.Kind, uid, err)
		return 0, err
	}
	rt.Pool.SetCreditDetail(uid, usable, expiring, unusable)
	log.Printf("credits refresh success platform=%s uid=%s remain=%d expiring=%d unusable=%d", rt.Kind, uid, usable, expiring, unusable)
	return usable, nil
}

// RefreshAll 刷新全部账号积分，返回汇总（供托盘消息框 / Web UI）。
func (a *App) RefreshAll() RefreshSummary {
	a.refreshMu.Lock()
	if a.refreshing {
		a.refreshMu.Unlock()
		return RefreshSummary{Busy: true}
	}
	a.refreshing = true
	a.refreshMu.Unlock()
	defer func() {
		a.refreshMu.Lock()
		a.refreshing = false
		a.refreshMu.Unlock()
	}()
	sum := RefreshSummary{Platforms: map[string]PlatformSummary{}}
	for _, rt := range a.runtimes {
		if rt == nil || rt.Pool == nil || rt.Upstream == nil {
			continue
		}
		ps := PlatformSummary{Accounts: []AccountRefresh{}}
		for _, st := range rt.Pool.List() {
			au := rt.Pool.AuthByUID(st.UID)
			if au == nil || au.AccessToken == "" {
				ps.Failed++
				ps.Accounts = append(ps.Accounts, AccountRefresh{UID: st.UID, OK: false, Msg: "no token"})
				continue
			}
			if usable, expiring, unusable, err := a.creditTotals(rt, au); err == nil {
				rt.Pool.SetCreditDetail(st.UID, usable, expiring, unusable)
				ps.OK++
				ps.Accounts = append(ps.Accounts, AccountRefresh{UID: st.UID, OK: true, Remain: usable})
			} else {
				ps.Failed++
				ps.Accounts = append(ps.Accounts, AccountRefresh{UID: st.UID, OK: false, Msg: shortErr(err)})
				log.Printf("credits refresh failed platform=%s uid=%s err=%v", rt.Kind, st.UID, err)
			}
		}
		sum.Total += ps.OK + ps.Failed
		sum.OK += ps.OK
		sum.Failed += ps.Failed
		sum.Platforms[rt.Kind.String()] = ps
	}
	return sum
}

// RefreshSummary 积分刷新汇总（托盘消息框内容）。
type RefreshSummary struct {
	Busy      bool                       `json:"busy"`
	Total     int                        `json:"total"`
	OK        int                        `json:"ok"`
	Failed    int                        `json:"failed"`
	Platforms map[string]PlatformSummary `json:"platforms"`
}

// PlatformSummary 单个渠道汇总。
type PlatformSummary struct {
	OK       int              `json:"ok"`
	Failed   int              `json:"failed"`
	Accounts []AccountRefresh `json:"accounts"`
}

// AccountRefresh 单账号刷新结果。
type AccountRefresh struct {
	UID    string `json:"uid"`
	OK     bool   `json:"ok"`
	Remain int64  `json:"remain"`
	Msg    string `json:"msg,omitempty"`
}

// RemoveAccount 删除账号（auth 文件 + 内存池）。
func (a *App) RemoveAccount(uid string) error {
	rt, au := a.findRuntimeAuth(uid)
	if rt == nil || au == nil {
		return fmt.Errorf("unknown account %s", uid)
	}
	if au.FilePath != "" {
		_ = os.Remove(au.FilePath)
	}
	rt.Pool.Remove(uid)
	log.Printf("已删除账号 %s", shortUID(uid))
	return nil
}

// DisableAccount 停用/启用账号。
func (a *App) DisableAccount(uid string, disabled bool) error {
	rt, _ := a.findRuntimeAuth(uid)
	if rt == nil || rt.Pool == nil {
		return fmt.Errorf("unknown account %s", uid)
	}
	rt.Pool.SetDisabled(uid, disabled)
	if disabled {
		log.Printf("已停用账号 %s", shortUID(uid))
	} else {
		log.Printf("已启用账号 %s", shortUID(uid))
	}
	return nil
}

// ResourceDetail 查询单个账号积分明细（含可用/不可用小计）。
// 返回的 remain 与 items 同时给出：remain 供 pool 口径对账，items 供 UI 明细展示。
// 遇 401 自动刷新 token 并重试一次，避免因本地 token 已失效导致明细永远为空。
func (a *App) ResourceDetail(uid string) (int64, []provider.ResourceItem, error) {
	rt, au := a.findRuntimeAuth(uid)
	if rt == nil || au == nil || rt.Upstream == nil {
		return 0, nil, fmt.Errorf("unknown account %s", uid)
	}
	remain, items, err := rt.Upstream.UserResourceDetail(au)
	if err != nil && a.refreshIfSessionDead(rt, au, err) {
		remain, items, err = rt.Upstream.UserResourceDetail(au)
	}
	return remain, items, err
}

// ---------------------------------------------------------------------------
// 配置操作
// ---------------------------------------------------------------------------

// SetCheckinTimes 更新自动签到时间（HH:MM）并立即唤醒两个调度器。
func (a *App) SetCheckinTimes(times []string) error {
	minutes, err := config.ParseClockTimes(times)
	if err != nil {
		return err
	}
	clean := normalizeMinutes(minutes)
	if len(clean) == 0 {
		return errors.New("请至少保留一个签到时间")
	}
	formatted := make([]string, 0, len(clean))
	for _, m := range clean {
		formatted = append(formatted, fmt.Sprintf("%02d:%02d", m/60, m%60))
	}
	a.mu.Lock()
	a.cfg.Schedule.CheckinTimes = formatted
	a.cfg.Schedule.CheckinHours = nil
	err = config.Save(a.cfg, a.cfgPath)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	for _, rt := range a.runtimes {
		if rt != nil && rt.Scheduler != nil {
			rt.Scheduler.SetCheckinMinutes(clean)
		}
	}
	log.Printf("自动签到时间已更新：%s", strings.Join(formatted, "、"))
	return nil
}

// SetListen 修改 API 监听主机 + 端口：保存配置并热切换监听。
func (a *App) SetListen(host string, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("端口无效：%d", port)
	}
	addr := config.Listen{Host: host, Port: port}.Addr()

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.serveLocked(addr); err != nil {
		return fmt.Errorf("监听 %s 失败（可能被占用）：%v", addr, err)
	}
	a.cfg.Listen = config.Listen{Host: host, Port: port}
	if err := config.Save(a.cfg, a.cfgPath); err != nil {
		log.Printf("save config after listen change: %v", err)
	}
	log.Printf("API 监听已切换至 %s", addr)
	return nil
}

// SetAPIKey 修改 API 密钥。
func (a *App) SetAPIKey(key string) error {
	key = strings.TrimSpace(key)
	a.mu.Lock()
	a.cfg.APIKey = key
	err := config.Save(a.cfg, a.cfgPath)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.handler.SetAPIKey(key)
	log.Printf("API-Key 已更新")
	return nil
}

// SetAPIKeyV2 修改 v2 API 密钥。
func (a *App) SetAPIKeyV2(key string) error {
	key = strings.TrimSpace(key)
	a.mu.Lock()
	a.cfg.APIKeyV2 = key
	err := config.Save(a.cfg, a.cfgPath)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.handler.SetAPIKeyV2(key)
	log.Printf("API-Key(v2) 已更新")
	return nil
}

// SetAutostart 设置/取消开机自启。
func (a *App) SetAutostart(on bool) error {
	if err := platform.SetAutostart(on); err != nil {
		return err
	}
	if on {
		log.Printf("已开启开机自启")
	} else {
		log.Printf("已关闭开机自启")
	}
	return nil
}

// AutostartEnabled 当前是否已开机自启。
func (a *App) AutostartEnabled() bool { return platform.AutostartEnabled() }

// ServerRunning 是否正在监听。
func (a *App) ServerRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.httpSrv != nil
}

// SetCompat 保存模型名路由配置（compat 段）并写回 config.json。
func (a *App) SetCompat(defaultChannel string, maxTokensCap int, modelMap map[string]string) error {
	if modelMap == nil {
		modelMap = map[string]string{}
	}
	for _, v := range modelMap {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("model_map 的值不能为空（应为 channel/model）")
		}
		if strings.Index(v, "/") <= 0 {
			return fmt.Errorf("model_map 值 %q 需为 channel/model 形式", v)
		}
	}
	a.mu.Lock()
	a.cfg.Compat.DefaultChannel = defaultChannel
	a.cfg.Compat.MaxTokensCap = maxTokensCap
	a.cfg.Compat.ModelMap = modelMap
	err := config.Save(a.cfg, a.cfgPath)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	// 热更新外层兼容层的路由表：不同步的话新映射要重启才生效。
	if a.compatSyncer != nil {
		a.compatSyncer(defaultChannel, maxTokensCap, modelMap)
	}
	log.Printf("模型名路由配置已更新：default_channel=%q max_tokens_cap=%d model_map=%d 条",
		defaultChannel, maxTokensCap, len(modelMap))
	return nil
}

// SetCompatSyncer 注入 compat 热更新回调（由 main.go 注入，指向 gateway.SetCompat）。
func (a *App) SetCompatSyncer(fn func(defaultChannel string, maxTokensCap int, modelMap map[string]string)) {
	a.compatSyncer = fn
}

// LoginBusy 是否登录中。
func (a *App) LoginBusy() bool {
	a.muLogin.Lock()
	defer a.muLogin.Unlock()
	return a.loginBusy
}

// ---------------------------------------------------------------------------
// 面板数据（Web UI）
// ---------------------------------------------------------------------------

// AccountView 面板展示的账号（脱敏）。
type AccountView struct {
	UID      string `json:"uid"`
	Group    string `json:"group"` // workbuddy | workbuddyai | traework | qoder
	Nickname string `json:"nickname"`
	// Credits 本工具可消耗的积分余额（pool 路由依据）。
	Credits int64 `json:"credits"`
	// ExpiringCredits 可消耗余额中 24h 内（到期日≤明天）到期的部分，仅面板展示。
	ExpiringCredits int64 `json:"expiring_credits,omitempty"`
	// UnusableCredits 账号名下有、但本工具用不了的积分（如 TraeWork ep=1 专用池），
	// 仅面板展示；0 表示该渠道不区分或没有此类额度。
	UnusableCredits int64 `json:"unusable_credits"`
	// CreditsStale 余额口径不可信（旧版 state 或尚未完成首次成功刷新），UI 显示「待刷新」。
	CreditsStale   bool   `json:"credits_stale,omitempty"`
	Cooling        bool   `json:"cooling"`
	Until           string `json:"until"`
	Reason          string `json:"reason"`
	Disabled        bool   `json:"disabled"`
	ErrCount        int    `json:"err_count"`
	LastCheckinOK   bool   `json:"last_checkin_ok"`
	LastCheckinAt   string `json:"last_checkin_at"`
	LastCheckinMsg  string `json:"last_checkin_msg"`
}

// State Web UI 初始数据。
type State struct {
	Accounts       []AccountView `json:"accounts"`
	CheckinTimes   []string      `json:"checkin_times"`
	KeepaliveHours []int         `json:"keepalive_hours"`
	ListenHost     string        `json:"listen_host"`
	ListenPort     int           `json:"listen_port"`
	APIKey         string        `json:"api_key"`
	APIKeyV2       string        `json:"api_key_v2"`
	LoginBusy      bool          `json:"login_busy"`
	NextCheckin    string        `json:"next_checkin"`
	Version        string        `json:"version"`
	Autostart      bool          `json:"autostart"`
	Running        bool          `json:"running"`

	// Compat 模型名路由配置（只读，保存走 POST /api/config/compat）
	Compat struct {
		DefaultChannel string            `json:"default_channel"`
		MaxTokensCap   int               `json:"max_tokens_cap"`
		ModelMap       map[string]string `json:"model_map"`
		Channels       []string          `json:"channels"` // 可用渠道列表（供 UI 下拉）
	} `json:"compat"`
}

// GetState 返回面板初始数据。
func (a *App) GetState() State {
	st := State{
		CheckinTimes:   a.checkinTimes(),
		KeepaliveHours: a.keepaliveHours(),
		ListenHost:     a.cfg.Listen.Host,
		ListenPort:     a.cfg.Listen.Port,
		APIKey:         a.cfg.APIKey,
		APIKeyV2:       a.cfg.APIKeyV2,
		LoginBusy:      a.LoginBusy(),
		NextCheckin:    fmtTime(a.nextFire()),
		Version:        Version,
		Autostart:      a.AutostartEnabled(),
		Running:        a.ServerRunning(),
	}
	st.Compat.DefaultChannel = a.cfg.Compat.DefaultChannel
	st.Compat.MaxTokensCap = a.cfg.Compat.MaxTokensCap
	st.Compat.ModelMap = a.cfg.Compat.ModelMap
	if st.Compat.ModelMap == nil {
		st.Compat.ModelMap = map[string]string{}
	}
	for k := range a.runtimes {
		st.Compat.Channels = append(st.Compat.Channels, k.String())
	}
	sort.Strings(st.Compat.Channels)
	st.Accounts = a.accountViews()
	return st
}

func (a *App) accountViews() []AccountView {
	statuses := a.allStatuses()
	out := make([]AccountView, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, AccountView{
			UID:             s.UID,
			Group:           a.accountGroup(s.UID),
			Nickname:        s.Nickname,
			Credits:         s.Credits,
			ExpiringCredits: s.ExpiringCredits,
			UnusableCredits: s.UnusableCredits,
			CreditsStale:    s.CreditsStale,
			Cooling:         s.Cooling,
			Until:           fmtTime(s.Until),
			Reason:          s.Reason,
			Disabled:        s.Disabled,
			ErrCount:        s.ErrCount,
			LastCheckinOK:   s.LastCheckinOK,
			LastCheckinAt:   fmtTime(s.LastCheckinAt),
			LastCheckinMsg:  s.LastCheckinMsg,
		})
	}
	return out
}

// accountGroup 返回账号所属分组（workbuddy/traework）。
func (a *App) accountGroup(uid string) string {
	_, au := a.findRuntimeAuth(uid)
	if au != nil {
		if au.Kind != "" {
			return au.Kind
		}
		if au.FilePath != "" && strings.HasPrefix(filepath.Base(au.FilePath), "trae-") {
			return "traework"
		}
		if au.FilePath != "" && strings.HasPrefix(filepath.Base(au.FilePath), "qoder") {
			return "qoder"
		}
		if au.FilePath != "" && strings.HasPrefix(filepath.Base(au.FilePath), "workbuddyai-") {
			return "workbuddyai"
		}
	}
	return "workbuddy"
}

// OpenLogFile 用系统默认编辑器打开日志文件。
func (a *App) OpenLogFile() error {
	fp := filepath.Join(filepath.Dir(a.cfg.StateFile), "app.log")
	if _, err := os.Stat(fp); err != nil {
		_ = os.WriteFile(fp, []byte("(empty)\n"), 0o644)
	}
	abs, err := filepath.Abs(fp)
	if err != nil {
		return err
	}
	return platform.OpenWithTextEditor(abs)
}

// NotifyCheckin 记录自动签到结果（调度器观察器）。
func (a *App) NotifyCheckin(platformName string, r scheduler.CheckinResult) {
	log.Printf("checkin platform=%s uid=%s ok=%t msg=%s remain=%d has_remain=%t",
		platformName, r.UID, r.OK, r.Msg, r.Remain, r.HasRemain)
}

// NotifyRefresh 记录 token 刷新结果（调度器观察器）。
func (a *App) NotifyRefresh(platformName, uid string, ok bool, msg string) {
	log.Printf("refresh platform=%s uid=%s ok=%t msg=%s", platformName, uid, ok, msg)
}

// ---------------------------------------------------------------------------
// 管理 API（HTTP）
// ---------------------------------------------------------------------------

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// apiError 统一错误响应。
func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// HandleAPI 注册管理 API 路由（挂到 server handler 的 /api/* 上）。
// 无鉴权（个人单机工具），监听 0.0.0.0 时风险由用户承担。

// benchResult 测速结果单条记录。
type benchResult struct {
	Model     string  `json:"model"`
	LatencyMs float64 `json:"latency_ms"`
	Status    string  `json:"status"`
	Error     string  `json:"error,omitempty"`
}

func (a *App) HandleAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.GetState())
	})
	mux.HandleFunc("POST /api/login/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Channel string `json:"channel"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		url, err := a.StartLoginFor(req.Channel)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"auth_url": url})
	})
	mux.HandleFunc("POST /api/login/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := a.CancelLogin(); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/account/checkin", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UID string `json:"uid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		res, err := a.CheckinAccount(req.UID)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/account/checkin_all", func(w http.ResponseWriter, r *http.Request) {
		results := a.CheckinAll()
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
	mux.HandleFunc("POST /api/account/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UID string `json:"uid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		remain, err := a.RefreshCredits(req.UID)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"remain": remain})
	})
	mux.HandleFunc("POST /api/account/refresh_all", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.RefreshAll())
	})
	mux.HandleFunc("POST /api/account/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UID string `json:"uid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.RemoveAccount(req.UID); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/account/disable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UID      string `json:"uid"`
			Disabled bool   `json:"disabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.DisableAccount(req.UID, req.Disabled); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/account/resource_detail", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UID string `json:"uid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		remain, items, err := a.ResourceDetail(req.UID)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		// 可用/不可用小计在服务端算好，前端只负责展示，避免两边口径漂移。
		usable, unusable := provider.Summarize(items)
		writeJSON(w, http.StatusOK, map[string]any{
			"remain":          remain,
			"items":           items,
			"usable_remain":   usable,
			"unusable_remain": unusable,
		})
	})
	mux.HandleFunc("POST /api/config/checkin_times", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Times []string `json:"times"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetCheckinTimes(req.Times); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/config/listen", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetListen(req.Host, req.Port); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/config/api_key", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetAPIKey(req.Key); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/config/api_key_v2", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetAPIKeyV2(req.Key); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/config/autostart", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetAutostart(req.On); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/config/compat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DefaultChannel string            `json:"default_channel"`
			MaxTokensCap   int               `json:"max_tokens_cap"`
			ModelMap       map[string]string `json:"model_map"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := a.SetCompat(req.DefaultChannel, req.MaxTokensCap, req.ModelMap); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/fees", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.FeesInfo())
	})
	mux.HandleFunc("POST /api/fees/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.FeesInfo())
		go a.safeGo(func() { a.RefreshPricing() })
	})
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		fp := filepath.Join(filepath.Dir(a.cfg.StateFile), "app.log")
		raw, _ := os.ReadFile(fp)
		lines := strings.Split(string(raw), "\n")
		if len(lines) > 300 {
			lines = lines[len(lines)-300:]
		}
		writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		go a.safeGo(func() { a.Quit() })
	})
	// GET /api/benchmark — 并发测速（有限并发 + 10s 超时 + 渠道级跳过）。
	// 结果逐条 SSE 推送，持久化到 benchmark-cache.json。
	mux.HandleFunc("GET /api/benchmark", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		modelsByKind := a.handler.ChannelModels()
		type task struct {
			modelID string
			kind    provider.Kind
			mi      provider.ModelInfo
		}
		type channelTasks struct {
			name  string
			tasks []task
		}
		var channels []channelTasks
		for kind, models := range modelsByKind {
			ct := channelTasks{name: kind.String()}
			for _, mi := range models {
				ct.tasks = append(ct.tasks, task{
					modelID: kind.String() + "/" + mi.ID,
					kind:    kind,
					mi:      mi,
				})
			}
			channels = append(channels, ct)
		}
		if len(channels) == 0 {
			fmt.Fprintf(w, "data: {\"done\":true,\"total\":0}\n\n")
			flusher.Flush()
			return
		}

		cached := make(map[string]benchResult)
		var mu sync.Mutex
		host := a.cfg.Listen.Host
		if host == "0.0.0.0" || host == "" || host == "::" {
			host = "127.0.0.1"
		}
		apiKey := a.cfg.APIKey
		port := a.cfg.Listen.Port
		client := &http.Client{Timeout: 10 * time.Second} // 10s 超时，超过的模型不可能是"最快"

		// 把全量任务摊平为切片
		var allTasks []task
		for _, ct := range channels {
			for _, t := range ct.tasks {
				allTasks = append(allTasks, t)
			}
		}
		total := len(allTasks)

		// 并发池：3 个 worker，以结果到达顺序推送 SSE
		type result struct {
			task  task
			res   benchResult
		}
		taskCh := make(chan task, total)
		resultCh := make(chan result, total)
		var wg sync.WaitGroup
		numWorkers := 3

		// 启动 worker
		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range taskCh {
					res := testOneModel(client, host, port, apiKey, t.modelID)
					resultCh <- result{t, res}
				}
			}()
		}

		// 发送任务
		go func() {
			for _, t := range allTasks {
				taskCh <- t
			}
			close(taskCh)
			wg.Wait()
			close(resultCh)
		}()

		// 消费结果：先出哪个模型就推送哪个（并发自然顺序）
		for res := range resultCh {
			mu.Lock()
			cached[res.task.modelID] = res.res
			mu.Unlock()
			b, _ := json.Marshal(res.res)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}

		// 持久化
		stateDir := filepath.Dir(a.cfg.StateFile)
		cachePath := filepath.Join(stateDir, "benchmark-cache.json")
		cache := map[string]any{
			"results":   cached,
			"tested_at": time.Now().Format(time.RFC3339),
		}
		if cb, err := json.MarshalIndent(cache, "", "  "); err == nil {
			_ = os.WriteFile(cachePath, cb, 0o644)
		}

		fmt.Fprintf(w, "data: {\"done\":true,\"total\":%d}\n\n", total)
		flusher.Flush()
	})

	// GET /api/benchmark/state — 上次测速结果
	mux.HandleFunc("GET /api/benchmark/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		stateDir := filepath.Dir(a.cfg.StateFile)
		cachePath := filepath.Join(stateDir, "benchmark-cache.json")
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"results": nil})
			return
		}
		var cache map[string]any
		if err := json.Unmarshal(raw, &cache); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"results": nil})
			return
		}
		writeJSON(w, http.StatusOK, cache)
	})
}

// testOneModel 测单个模型 TTFT。
func testOneModel(client *http.Client, host string, port int, apiKey string, modelID string) benchResult {
	start := time.Now()
	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":true}`, modelID)
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://%s:%d/v1/chat/completions", host, port),
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		lat := float64(time.Since(start).Milliseconds())
		return benchResult{Model: modelID, LatencyMs: lat, Status: "error", Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		lat := float64(time.Since(start).Milliseconds())
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		return benchResult{Model: modelID, LatencyMs: lat, Status: "error",
			Error: fmt.Sprintf("HTTP %d %s", resp.StatusCode, string(buf[:min(n, 100)]))}
	}

	// 逐行解析 SSE 直到找到首个内容 delta
	br := bufio.NewReaderSize(resp.Body, 64*1024)
	firstContentLat := float64(0)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if firstContentLat == 0 {
				firstContentLat = float64(time.Since(start).Milliseconds())
			}
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			if firstContentLat == 0 {
				firstContentLat = float64(time.Since(start).Milliseconds())
			}
			break
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c, _ := choices[0].(map[string]any)
		if c == nil {
			continue
		}
		delta, _ := c["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		content, _ := delta["content"].(string)
		reasoning, _ := delta["reasoning_content"].(string)
		if content == "" && reasoning == "" {
			continue
		}
		firstContentLat = float64(time.Since(start).Milliseconds())
		break
	}
	return benchResult{Model: modelID, LatencyMs: firstContentLat, Status: "ok"}
}

// benchResult 测速结果单条记录。

// ---------------------------------------------------------------------------
// 渠道费率信息（本地缓存 + 按需刷新）
// ---------------------------------------------------------------------------

// feesChannel 一个渠道在「模型列表和费率」面板中的分组数据。
type feesChannel struct {
	Channel string         `json:"channel"`
	Models  []feesModelRow `json:"models"`
}

// feesModelRow 一行模型：来自渠道 models 列表，尽可能配上费率。
type feesModelRow struct {
	Model string `json:"model"`
	// Priced 表示上游费率接口对该模型返回了可用的倍率值。
	// 注意：credits 字段缺失（如 CN 的 auto 调度器）也算未定价，
	// 不能把「缺字段解析出的 0」当成免费。
	Priced bool    `json:"priced"`
	Rate   float64 `json:"rate"`           // 倍率（Priced=false 时无意义）
	Free   bool    `json:"free"`           // 上游明确标注倍率为 0（x0.00）
	Note   string  `json:"note,omitempty"` // 促销/标签说明（已剔除颜色）
	// Color 为促销标签颜色（形如 "#FF0000"），无则空。
	Color string `json:"color,omitempty"`
	// HasContext 标记该模型的上下文窗口/最大输出是否来自上游接口。
	// false 表示无可靠数据（硬编码估算或上游不返回），前端提示「未知」而非展示数字。
	HasContext    bool  `json:"has_context"`
	ContextWindow int64 `json:"context_window,omitempty"`
	MaxTokens     int64 `json:"max_tokens,omitempty"`
	// 能力标记（与 /v1/models 同源），供 UI 在模型 ID 后展示图标。
	SupportsImages    bool `json:"supports_images"`
	SupportsReasoning bool `json:"supports_reasoning"`
	SupportsTools     bool `json:"supports_tools"`
}

// buildFeesChannels 以「渠道 models 列表」为基准组表：
//   - 每个渠道的模型行来自该渠道当前生效的 models（与 /v1/models 同源）；
//   - 再用该渠道费率接口返回的 model→rate 映射逐行填充；
//   - 未在费率接口中出现的模型标记 Priced=false（前端显示 unknown + tooltip）；
//   - 费率接口中多出的模型（models 列表中不存在）不展示，避免与 /v1/models 不一致。
func buildFeesChannels(modelsByKind map[provider.Kind][]provider.ModelInfo,
	pricing []provider.ModelPricing, order []provider.Kind) []feesChannel {
	// 渠道 → (model → pricing)
	byChannel := make(map[string]map[string]provider.ModelPricing)
	for _, p := range pricing {
		m := byChannel[p.Channel]
		if m == nil {
			m = make(map[string]provider.ModelPricing)
			byChannel[p.Channel] = m
		}
		m[p.Model] = p
	}
	out := make([]feesChannel, 0, len(order))
	for _, k := range order {
		infos := modelsByKind[k]
		if len(infos) == 0 {
			continue
		}
		prices := byChannel[k.String()]
		rows := make([]feesModelRow, 0, len(infos))
		for _, mi := range infos {
			row := feesModelRow{
				Model:         mi.ID,
				ContextWindow: mi.ContextWindow,
				MaxTokens:     mi.MaxTokens,
				// 仅上游接口返回的上下文可信；硬编码估算值不展示数字。
				HasContext: mi.ContextFromAPI,
				// 能力标记与 /v1/models 同源，保证 UI 图标与接口声明一致。
				SupportsImages:    mi.SupportsImages,
				SupportsReasoning: mi.SupportsReasoning,
				SupportsTools:     mi.SupportsTools,
			}
			if p, ok := prices[mi.ID]; ok {
				row.Priced = p.IsExplicit() // 缺倍率字段（如 auto）不算已定价
				row.Rate = p.Rate
				row.Free = p.IsExplicit() && p.Rate == 0
				row.Note = p.Note
				row.Color = p.Color
			}
			rows = append(rows, row)
		}
		// 排序：免费在前（便于发现可用免费模型），其余按倍率升序，未定价置后
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.Priced != b.Priced {
				return a.Priced // 已定价的排前
			}
			if a.Rate != b.Rate {
				return a.Rate < b.Rate
			}
			return a.Model < b.Model
		})
		out = append(out, feesChannel{Channel: k.String(), Models: rows})
	}
	return out
}

// FeesInfo 返回「模型列表和费率」面板数据。
// 以各渠道当前生效的模型列表为基准（与 /v1/models 同源），逐行配上费率：
// 上游未返回倍率的模型标记 priced=false，前端显示 unknown。
func (a *App) FeesInfo() map[string]any {
	a.pricingMu.Lock()
	cached := a.pricingCache
	errMsg := a.pricingErr
	stale := time.Since(a.pricingFetched) > time.Hour
	a.pricingMu.Unlock()

	// 缓存过期时后台静默刷新
	if len(cached) > 0 && stale {
		go a.safeGo(func() { a.RefreshPricing() })
	}

	// 模型列表：优先用 server 的渠道清单（与 /v1/models 完全一致）
	var modelsByKind map[provider.Kind][]provider.ModelInfo
	if a.handler != nil {
		modelsByKind = a.handler.ChannelModels()
	} else {
		modelsByKind = a.staticModelFallback()
	}
	if len(modelsByKind) == 0 {
		modelsByKind = a.staticModelFallback()
	}

	// 首次加载且无缓存：异步拉取费率，先展示（unknown 占位）
	if len(cached) == 0 {
		go a.safeGo(func() { a.RefreshPricing() })
	}

	channels := buildFeesChannels(modelsByKind, cached, []provider.Kind{
		provider.WorkBuddy, provider.WorkBuddyAI, provider.TraeWork, provider.Qoder, provider.QoderCN, provider.QoderCOM, provider.QwenWork,
	})

	result := map[string]any{
		"note":       "本表以各渠道实际可用模型列表为准；倍率为空的模型表示上游未返回定价（未知）。",
		"channels":   channels,
		"disclaimer": "本工具仅聚合转发，不参与定价；渠道费率以各上游官方页面为准。",
	}
	if len(cached) > 0 {
		result["cached_at"] = a.pricingFetched.Format("01-02 15:04")
	}
	if errMsg != "" {
		result["error"] = errMsg
	}
	return result
}

// staticModelFallback 无 handler 时的渠道→静态模型表兜底。
// app.Runtime 不带静态表（那是 server.Runtime 的字段），故此处返回空，
// 由 buildFeesChannels 自行跳过无模型的渠道；
// 实际运行中 handler 总是已注入（SetHandler），不会走到这里。
func (a *App) staticModelFallback() map[provider.Kind][]provider.ModelInfo {
	return map[provider.Kind][]provider.ModelInfo{}
}

// RefreshPricing 从所有已接入渠道拉取最新定价并更新缓存。
// 并发调用会被丢弃（已有一次在跑）——启动刷新与面板「刷新」可能重叠。
func (a *App) RefreshPricing() {
	a.pricingMu.Lock()
	if a.pricingBusy {
		a.pricingMu.Unlock()
		return
	}
	a.pricingBusy = true
	a.pricingMu.Unlock()
	defer func() {
		a.pricingMu.Lock()
		a.pricingBusy = false
		a.pricingMu.Unlock()
	}()

	allPricing := make([]provider.ModelPricing, 0)
	var errs []string

	for _, rt := range a.runtimes {
		if rt == nil || rt.Pool == nil || rt.Upstream == nil || len(rt.Pool.List()) == 0 {
			continue
		}
		acct := rt.Pool.Pick()
		if acct == nil {
			continue
		}
		// 先确保 token 有效
		if acct.NeedsRefresh(10 * time.Minute) {
			if err := rt.Upstream.RefreshToken(acct); err != nil {
				errs = append(errs, fmt.Sprintf("%s: token refresh failed", rt.Kind))
				continue
			}
			// 必须落盘：refresh token 会轮换，不写回则下次启动用的是旧 refresh token，
			// 而旧 access token 已被上游作废 → 本地 expiresAt 仍显示有效 → 卡死在 401。
			if serr := acct.SaveAtomic(); serr != nil {
				log.Printf("pricing token save failed platform=%s uid=%s err=%v", rt.Kind, acct.UID, serr)
			}
		}
		pricing, err := rt.Upstream.FetchModelPricing(acct)
		if err != nil {
			// 401 同样先刷新再重试一次，避免费率因过期 token 长期拉不到。
			if a.refreshIfSessionDead(rt, acct, err) {
				pricing, err = rt.Upstream.FetchModelPricing(acct)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", rt.Kind, shortErr(err)))
			log.Printf("pricing fetch failed platform=%s err=%v", rt.Kind, err)
			continue
		}
		allPricing = append(allPricing, pricing...)
		log.Printf("pricing fetched platform=%s count=%d", rt.Kind, len(pricing))
	}

	// 排序：按渠道 + 倍率
	sort.Slice(allPricing, func(i, j int) bool {
		if allPricing[i].Channel != allPricing[j].Channel {
			return allPricing[i].Channel < allPricing[j].Channel
		}
		return allPricing[i].Rate < allPricing[j].Rate
	})

	a.pricingMu.Lock()
	a.pricingCache = allPricing
	a.pricingFetched = time.Now()
	if len(errs) > 0 {
		a.pricingErr = strings.Join(errs, "; ")
	} else {
		a.pricingErr = ""
	}
	a.pricingMu.Unlock()
	a.savePricingCache()
}

// loadPricingCache 从文件加载定价缓存。
func (a *App) loadPricingCache() {
	raw, err := os.ReadFile(a.pricingFP)
	if err != nil {
		return
	}
	var cache struct {
		Models  []provider.ModelPricing `json:"models"`
		Fetched string                  `json:"fetched"`
	}
	if err := json.Unmarshal(raw, &cache); err != nil {
		return
	}
	a.pricingMu.Lock()
	a.pricingCache = cache.Models
	if cache.Fetched != "" {
		if t, err := time.Parse(time.RFC3339, cache.Fetched); err == nil {
			a.pricingFetched = t
		}
	}
	a.pricingMu.Unlock()
}

// savePricingCache 持久化定价缓存到文件。
func (a *App) savePricingCache() {
	a.pricingMu.Lock()
	cache := struct {
		Models  []provider.ModelPricing `json:"models"`
		Fetched string                  `json:"fetched"`
	}{
		Models:  a.pricingCache,
		Fetched: a.pricingFetched.Format(time.RFC3339),
	}
	a.pricingMu.Unlock()
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(a.pricingFP, raw, 0o644)
}

// ---------------------------------------------------------------------------
// 日志
// ---------------------------------------------------------------------------

// logWriter 写日志文件。
type logWriter struct{ app *App }

func (w *logWriter) Write(p []byte) (int, error) {
	if w.app.logFile != nil {
		_, _ = w.app.logFile.Write(p)
	}
	return len(p), nil
}

// safeGo 带 panic 兜底的 goroutine 启动器。
func (a *App) safeGo(f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC: %v", r)
			}
		}()
		f()
	}()
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func normalizeMinutes(minutes []int) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, m := range minutes {
		if m >= 0 && m < 24*60 && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Ints(out)
	return out
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("01-02 15:04")
}

func shortUID(uid string) string {
	if len(uid) <= 10 {
		return uid
	}
	return uid[:10] + "…"
}

func shortErr(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
