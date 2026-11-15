// Package openxml 提供 C# OpenXmlHelper 的 Go 客户端。
//
// 设计依据：openxmlhelper/开发说明.md
//
// 协议（语言无关，Go 直接复用）：
//
//	Main → 助手（stdin）: {"v":1,"type":"run","job":"<jobId>"}
//	助手 → Main（stdout）: {"v":1,"type":"done","job":"<jobId>","ok":true}
//
//	任务参数与结果通过文件交换，不走管道：
//	  <workspace>/openxml-jobs/<job>/request.json   （Go 写）
//	  <workspace>/openxml-jobs/<job>/result.json    （助手写）
//
// **为什么用进程池**：
//
//	助手是长驻进程，一次只跑一个任务（忙时后到的任务直接失败）。
//	Web 多用户并发时必须有池。每个进程独占一个工作目录。
//
// **为什么 Linux 可用**：
//
//	助手是纯 Open XML 结构操作，零 DllImport / 零 COM / 零平台分支。
//	已实测：81MB 单文件自包含，stdio 协议跑通。
package openxml

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 错误。
var (
	ErrPoolClosed   = errors.New("进程池已关闭")
	ErrJobTimeout   = errors.New("任务执行超时")
	ErrNoAvailable  = errors.New("没有可用的助手进程")
	ErrHelperFailed = errors.New("助手执行失败")
)

// Config 是客户端配置。
type Config struct {
	// Binary 是 openxmlhelper 可执行文件路径
	Binary string
	// PoolSize 进程池大小（每进程约 100-200MB 内存）
	PoolSize int
	// JobTimeout 单个任务超时
	JobTimeout time.Duration
	// StartTimeout 进程启动超时
	StartTimeout time.Duration
}

// Client 管理 C# 助手的进程池。
type Client struct {
	cfg Config
	log *slog.Logger

	mu     sync.Mutex
	procs  chan *helperProc
	closed bool
	// 所有进程的引用，关闭时统一杀
	all []*helperProc
}

// helperProc 是一个助手进程。
type helperProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	workdir string
	// mu 保证一次只跑一个任务
	mu     sync.Mutex
	closed bool
}

// NewClient 创建客户端并预热进程池。
//
// 预热是必要的：第一个任务时启动进程会多花 1-2 秒。
func NewClient(ctx context.Context, cfg Config, log *slog.Logger) (*Client, error) {
	if cfg.Binary == "" {
		return nil, fmt.Errorf("未配置 openxmlhelper 路径")
	}
	if _, err := os.Stat(cfg.Binary); err != nil {
		return nil, fmt.Errorf("openxmlhelper 不存在: %w", err)
	}
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 4
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 5 * time.Minute
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 30 * time.Second
	}

	c := &Client{
		cfg:   cfg,
		log:   log,
		procs: make(chan *helperProc, cfg.PoolSize),
	}

	// 预热：启动全部进程
	for i := 0; i < cfg.PoolSize; i++ {
		p, err := c.startProc(ctx, i)
		if err != nil {
			// 启动失败时清理已启动的
			c.Close()
			return nil, fmt.Errorf("启动助手进程 %d 失败: %w", i, err)
		}
		c.all = append(c.all, p)
		c.procs <- p
	}

	log.Info("OpenXmlHelper 进程池已就绪", slog.Int("size", cfg.PoolSize))
	return c, nil
}

// startProc 启动一个助手进程。
func (c *Client) startProc(ctx context.Context, index int) (*helperProc, error) {
	// 每个进程独立工作目录，避免互相干扰
	workdir, err := os.MkdirTemp("", fmt.Sprintf("openxml-%d-*", index))
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "openxml-jobs"), 0o755); err != nil {
		return nil, err
	}

	cmd := exec.Command(c.cfg.Binary, "--workspace", workdir)
	// 助手需要 UTF-8 环境
	cmd.Env = append(os.Environ(), "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=false")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	// stderr 收集起来便于排查
	var stderrBuf syncBuffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动进程失败: %w", err)
	}

	p := &helperProc{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		workdir: workdir,
	}

	// 健康检查：发 ping 确认能响应
	if err := p.ping(c.cfg.StartTimeout); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("助手无响应: %w（stderr: %s）", err, stderrBuf.String())
	}

	return p, nil
}

// ExecuteOptions 是执行选项。
type ExecuteOptions struct {
	// Files 需要在执行前写入助手工作区的文件。
	// key 是工作区内的相对路径（与 request 里的 input/source 对应），
	// value 是本地绝对路径。
	//
	// 为什么需要：助手的 input/source 是「工作区内相对路径」，
	// 而我们手上的文件在别处，必须先物化进它能看到的位置。
	Files map[string]string

	// CollectBack 需要在执行后取回的文件（工作区相对路径 → 本地保存路径）。
	// 用于 insert-body-html 这类「原地修改 input 文件」的动作。
	CollectBack map[string]string

	// CollectJobFiles 需要在执行后从**任务目录**取回的文件
	// （任务目录内相对路径 → 本地保存路径）。
	//
	// 为什么需要单独一个字段：有些 action 把结果写在 job 子目录里
	// （如 blind-check-inspect 的 blind-check-result.json），
	// 而不是工作区根目录。jobId 由 client 内部生成，调用方不知道，
	// 所以只能用这个字段声明"我要从 job 目录取这个文件"。
	CollectJobFiles map[string]string
}

// Execute 执行一个动作（无文件准备）。
func (c *Client) Execute(ctx context.Context, req map[string]any) (*Result, error) {
	return c.ExecuteWithFiles(ctx, req, ExecuteOptions{})
}

// ExecuteWithFiles 执行动作并处理文件进出。
func (c *Client) ExecuteWithFiles(ctx context.Context, req map[string]any, opts ExecuteOptions) (*Result, error) {
	// 取一个空闲进程
	var p *helperProc
	select {
	case p = <-c.procs:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(c.cfg.StartTimeout):
		return nil, ErrNoAvailable
	}

	// 用完归还
	defer func() {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if !closed && !p.closed {
			c.procs <- p
		}
	}()

	// 准备输入文件
	if len(opts.Files) > 0 {
		if err := p.prepareFiles(opts.Files); err != nil {
			return nil, err
		}
	}

	// 执行
	res, err := p.execute(ctx, req, c.cfg.JobTimeout, opts.CollectJobFiles)

	// 取回产物（失败也要取，便于排查）
	if len(opts.CollectBack) > 0 {
		if collectErr := p.collectFiles(opts.CollectBack); collectErr != nil {
			c.log.Warn("取回产物失败", slog.String("error", collectErr.Error()))
		}
	}

	return res, err
}

// prepareFiles 把文件写入工作区。
func (p *helperProc) prepareFiles(files map[string]string) error {
	for rel, src := range files {
		// 拒绝路径穿越
		if strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("非法的工作区相对路径: %s", rel)
		}

		dst := filepath.Join(p.workdir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}

		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("写入工作区失败: %w", err)
		}
	}
	return nil
}

// collectFiles 把工作区文件取回到本地。
func (p *helperProc) collectFiles(files map[string]string) error {
	for rel, dst := range files {
		if strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			continue
		}

		src := filepath.Join(p.workdir, filepath.FromSlash(rel))
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("读取工作区 %s 失败: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("写回 %s 失败: %w", dst, err)
		}
	}
	return nil
}

// execute 在单个进程上执行任务。
func (p *helperProc) execute(ctx context.Context, req map[string]any, timeout time.Duration, jobFiles map[string]string) (*Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, ErrHelperFailed
	}

	// 生成任务 ID（只允许字母数字点横线下划线）
	jobID := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), randSuffix())
	jobDir := filepath.Join(p.workdir, "openxml-jobs", jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建任务目录失败: %w", err)
	}
	defer os.RemoveAll(jobDir)

	// 写请求
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "request.json"), reqData, 0o644); err != nil {
		return nil, fmt.Errorf("写 request.json 失败: %w", err)
	}

	// 发送 run 信号
	signal := fmt.Sprintf("{\"v\":1,\"type\":\"run\",\"job\":%q}\n", jobID)
	if _, err := p.stdin.Write([]byte(signal)); err != nil {
		p.closed = true
		return nil, fmt.Errorf("发送任务信号失败: %w", err)
	}

	// 等待 done 信号（带超时）
	doneCh := make(chan error, 1)
	go func() {
		line, err := p.stdout.ReadString('\n')
		if err != nil {
			doneCh <- fmt.Errorf("读取响应失败: %w", err)
			return
		}
		var resp struct {
			Type string `json:"type"`
			Job  string `json:"job"`
			OK   bool   `json:"ok"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			doneCh <- fmt.Errorf("解析响应失败: %w（原文：%s）", err, textutil.Truncate(line, 200))
			return
		}
		if resp.Job != jobID {
			doneCh <- fmt.Errorf("任务 ID 不匹配：期望 %s，实际 %s", jobID, resp.Job)
			return
		}
		if !resp.OK {
			doneCh <- ErrHelperFailed
			return
		}
		doneCh <- nil
	}()

	select {
	case err := <-doneCh:
		if err != nil && errors.Is(err, ErrHelperFailed) {
			// 读 result.json 拿具体错误
			res, readErr := readResult(jobDir)
			if readErr == nil && res.Error != "" {
				return nil, fmt.Errorf("助手返回失败: %s", res.Error)
			}
			return nil, err
		}
		if err != nil {
			p.closed = true
			return nil, err
		}
	case <-ctx.Done():
		p.closed = true
		return nil, ctx.Err()
	case <-time.After(timeout):
		p.closed = true
		return nil, ErrJobTimeout
	}

	// 从 job 目录取回结果文件（如 blind-check-result.json）
	for rel, dst := range jobFiles {
		if strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(jobDir, rel))
		if readErr != nil {
			return nil, fmt.Errorf("读取任务产物 %s 失败: %w", rel, readErr)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, fmt.Errorf("写回 %s 失败: %w", dst, err)
		}
	}

	return readResult(jobDir)
}

// ping 健康检查。
func (p *helperProc) ping(timeout time.Duration) error {
	jobID := fmt.Sprintf("ping-%d", time.Now().UnixMilli())
	jobDir := filepath.Join(p.workdir, "openxml-jobs", jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(jobDir)

	if err := os.WriteFile(filepath.Join(jobDir, "request.json"), []byte(`{"action":"ping"}`), 0o644); err != nil {
		return err
	}

	signal := fmt.Sprintf("{\"v\":1,\"type\":\"run\",\"job\":%q}\n", jobID)
	if _, err := p.stdin.Write([]byte(signal)); err != nil {
		return err
	}

	doneCh := make(chan error, 1)
	go func() {
		line, err := p.stdout.ReadString('\n')
		if err != nil {
			doneCh <- err
			return
		}
		var resp struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			doneCh <- err
			return
		}
		if !resp.OK {
			doneCh <- ErrHelperFailed
			return
		}
		doneCh <- nil
	}()

	select {
	case err := <-doneCh:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("ping 超时")
	}
}

// Result 是助手返回的结果。
type Result struct {
	OK         bool   `json:"ok"`
	Action     string `json:"action"`
	Error      string `json:"error"`
	ErrorKind  string `json:"errorKind"`
	Output     any    `json:"output"`
	BlockCount *int   `json:"blockCount"`
	// Raw 是原始 JSON，便于取特定字段
	Raw json.RawMessage `json:"-"`
}

// readResult 读 result.json。
func readResult(jobDir string) (*Result, error) {
	data, err := os.ReadFile(filepath.Join(jobDir, "result.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 result.json 失败: %w", err)
	}

	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("解析 result.json 失败: %w", err)
	}
	res.Raw = data

	return &res, nil
}

// Close 关闭全部进程。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	close(c.procs)

	for _, p := range c.all {
		p.mu.Lock()
		p.closed = true
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
		// 清理工作目录
		_ = os.RemoveAll(p.workdir)
		p.mu.Unlock()
	}

	c.log.Info("OpenXmlHelper 进程池已关闭")
	return nil
}

// Health 检查至少有一个进程可用。
func (c *Client) Health(ctx context.Context) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrPoolClosed
	}

	select {
	case p := <-c.procs:
		c.procs <- p
		return nil
	default:
		return ErrNoAvailable
	}
}

// --- 内部工具 ---

// syncBuffer 是并发安全的 buffer。
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	// 限制大小，避免无限增长
	if len(b.buf) > 8192 {
		b.buf = b.buf[len(b.buf)-4096:]
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// randSuffix 生成 6 位随机后缀。
func randSuffix() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	// 用时间纳秒做种子，避免引入 crypto/rand
	n := time.Now().UnixNano()
	for i := range b {
		b[i] = chars[n%int64(len(chars))]
		n /= int64(len(chars))
	}
	return string(b)
}
