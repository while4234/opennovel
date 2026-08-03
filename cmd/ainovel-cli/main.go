package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/entry/headless"
	"github.com/voocel/ainovel-cli/internal/entry/web"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "authority" {
		if err := runAuthorityCommand(os.Args[2:]); err != nil {
			die("authority: %v", err)
		}
		return
	}
	opts, args, err := parseCLIOptions(os.Args[1:])
	if err != nil {
		die("flags: %v", err)
	}
	if opts.Version {
		buildversion.Print(os.Stdout, versionInfo())
		return
	}
	if opts.Update {
		if err := runSelfUpdate(opts.UpdateVersion); err != nil {
			fmt.Fprintf(os.Stderr, "update: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		die("config: %v", err)
	}
	runWithConfig(cfg, opts, args)
}

func runAuthorityCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected init, rotate, revoke, export, import, or gc")
	}
	switch args[0] {
	case "init":
		if len(args) != 1 {
			return fmt.Errorf("init accepts no arguments")
		}
		keyID, err := store.InitializeExpansionAuthorityRoot()
		if err == nil {
			fmt.Fprintf(os.Stdout, "authority initialized: %s\n", keyID)
		}
		return err
	case "rotate":
		if len(args) != 1 {
			return fmt.Errorf("rotate accepts no arguments")
		}
		return store.RotateExpansionAuthorityRoot()
	case "revoke":
		if len(args) != 2 {
			return fmt.Errorf("revoke requires a key id")
		}
		return store.RevokeExpansionAuthorityRootKey(args[1])
	case "gc":
		if len(args) != 1 {
			return fmt.Errorf("gc accepts no arguments")
		}
		report, err := store.ReconcileExpansionAuthorityOrphans()
		if err == nil {
			fmt.Fprintf(os.Stdout, "authority maintenance: examined=%d recovered=%d finalized=%d deferred=%d\n", report.Examined, report.Recovered, report.Finalized, report.Deferred)
		}
		return err
	case "export", "import":
		if len(args) != 3 {
			return fmt.Errorf("%s requires an absolute bundle path and a 32-byte key file", args[0])
		}
		key, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		if len(key) != 32 {
			return fmt.Errorf("wrapping key file must contain exactly 32 bytes")
		}
		if args[0] == "export" {
			return store.ExportExpansionAuthorityRoot(args[1], key)
		}
		return store.ImportExpansionAuthorityRoot(args[1], key)
	default:
		return fmt.Errorf("unknown authority command %q", args[0])
	}
}

func die(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	if path := bootstrap.WriteStartupError(msg); path != "" {
		fmt.Fprintf(os.Stderr, "（详细错误已记录到 %s）\n", path)
	}
	os.Exit(1)
}

func runWithConfig(cfg bootstrap.Config, opts cliOptions, args []string) {
	rules.EnsureHomeRulesDir()
	bundle := assets.Load(cfg.Style)

	if opts.Web {
		if len(args) > 0 {
			die("error: web 不接受位置参数: %s", strings.Join(args, " "))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := web.Run(ctx, cfg, bundle, web.Options{
			Host:        opts.WebHost,
			Port:        opts.WebPort,
			RuntimeRoot: opts.WebRuntimeRoot,
			Open:        opts.WebOpen,
			Stdout:      os.Stdout,
			Stderr:      os.Stderr,
		}); err != nil {
			die("web: %v", err)
		}
		return
	}

	if len(args) > 0 {
		die("error: headless 不接受位置参数，请使用 --prompt 或 --prompt-file")
	}
	prompt, err := loadPrompt(opts)
	if err != nil {
		die("error: %v", err)
	}
	answers, err := headless.LoadAnswersFile(opts.AnswersFile, os.Stdin)
	if err != nil {
		die("%s", headless.FormatError(err))
	}
	if err := headless.Run(cfg, bundle, headless.Options{
		Prompt:             prompt,
		Answers:            answers,
		AdaptPath:          opts.AdaptPath,
		AdaptGranularity:   opts.AdaptGranularity,
		AdaptRewritePolicy: opts.AdaptRewritePolicy,
		AdaptWordTolerance: opts.AdaptWordTolerance,
	}); err != nil {
		die("%s", headless.FormatError(err))
	}
}

type cliOptions struct {
	ConfigPath         string
	Headless           bool
	Prompt             string
	PromptFile         string
	AnswersFile        string
	AdaptPath          string
	AdaptGranularity   string
	AdaptRewritePolicy string
	AdaptWordTolerance float64
	Version            bool
	Update             bool
	UpdateVersion      string
	Web                bool
	WebHost            string
	WebPort            int
	WebRuntimeRoot     string
	WebOpen            bool
}

func parseCLIOptions(argv []string) (cliOptions, []string, error) {
	var opts cliOptions
	var args []string
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--version", "-v":
			opts.Version = true
		case "version":
			if i+1 < len(argv) {
				return opts, nil, fmt.Errorf("version 不接受参数")
			}
			opts.Version = true
		case "update":
			if opts.Update {
				return opts, nil, fmt.Errorf("update 只能指定一次")
			}
			opts.Update = true
			if i+1 < len(argv) {
				if strings.HasPrefix(argv[i+1], "-") {
					return opts, nil, fmt.Errorf("update 只接受一个可选版本参数")
				}
				opts.UpdateVersion = argv[i+1]
				i++
			}
			if i+1 < len(argv) {
				return opts, nil, fmt.Errorf("update 只接受一个可选版本参数")
			}
		case "web":
			if opts.Version || opts.Update || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || opts.AnswersFile != "" || opts.AdaptPath != "" || opts.AdaptGranularity != "" || opts.AdaptRewritePolicy != "" || opts.AdaptWordTolerance > 0 || len(args) > 0 {
				return opts, nil, fmt.Errorf("web 不能与其他启动模式或位置参数混用")
			}
			opts.Web = true
			opts.WebHost = web.DefaultHost
			opts.WebPort = web.DefaultPort
			if err := parseWebOptions(&opts, argv[i+1:]); err != nil {
				return opts, nil, err
			}
			return opts, nil, nil
		case "--config":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--config 缺少值")
			}
			opts.ConfigPath = argv[i+1]
			i++
		case "--headless":
			opts.Headless = true
		case "--prompt":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt 缺少值")
			}
			opts.Prompt = argv[i+1]
			i++
		case "--prompt-file":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--prompt-file 缺少值")
			}
			opts.PromptFile = argv[i+1]
			i++
		case "--answers-file":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--answers-file 缺少值")
			}
			opts.AnswersFile = argv[i+1]
			i++
		case "--adapt":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--adapt 缺少值")
			}
			opts.AdaptPath = argv[i+1]
			i++
		case "--adapt-granularity":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--adapt-granularity 缺少值")
			}
			opts.AdaptGranularity = argv[i+1]
			i++
		case "--adapt-rewrite-policy":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--adapt-rewrite-policy 缺少值")
			}
			opts.AdaptRewritePolicy = argv[i+1]
			i++
		case "--adapt-word-tolerance":
			if i+1 >= len(argv) {
				return opts, nil, fmt.Errorf("--adapt-word-tolerance 缺少值")
			}
			value, err := strconv.ParseFloat(argv[i+1], 64)
			if err != nil || value <= 0 {
				return opts, nil, fmt.Errorf("--adapt-word-tolerance 必须是大于 0 的数字")
			}
			opts.AdaptWordTolerance = value
			i++
		default:
			args = append(args, argv[i])
		}
	}
	if err := validateCLIOptions(&opts, args); err != nil {
		return opts, nil, err
	}
	return opts, args, nil
}

func validateCLIOptions(opts *cliOptions, args []string) error {
	if opts.Prompt != "" && opts.PromptFile != "" {
		return fmt.Errorf("--prompt 和 --prompt-file 不能同时使用")
	}
	if opts.PromptFile == "-" && opts.AnswersFile == "-" {
		return fmt.Errorf("--prompt-file - 与 --answers-file - 不能同时读取标准输入")
	}
	if opts.Version && (opts.Update || opts.ConfigPath != "" || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || opts.AnswersFile != "" || opts.AdaptPath != "" || len(args) > 0) {
		return fmt.Errorf("version 不能与其他启动参数混用")
	}
	if opts.Update && (opts.ConfigPath != "" || opts.Headless || opts.Prompt != "" || opts.PromptFile != "" || opts.AnswersFile != "" || opts.AdaptPath != "" || len(args) > 0) {
		return fmt.Errorf("update 不能与其他启动参数混用")
	}
	if !opts.Headless && (opts.Prompt != "" || opts.PromptFile != "" || opts.AnswersFile != "" || opts.AdaptPath != "" || opts.AdaptGranularity != "" || opts.AdaptRewritePolicy != "" || opts.AdaptWordTolerance > 0) {
		return fmt.Errorf("--prompt/--prompt-file/--answers-file/--adapt 只能在 --headless 模式下使用")
	}
	if opts.AdaptPath != "" && opts.Prompt == "" && opts.PromptFile == "" {
		return fmt.Errorf("--adapt 需要同时提供 --prompt 或 --prompt-file 作为改编 brief")
	}
	if opts.AdaptPath == "" && (opts.AdaptGranularity != "" || opts.AdaptRewritePolicy != "" || opts.AdaptWordTolerance > 0) {
		return fmt.Errorf("--adapt-granularity/--adapt-rewrite-policy/--adapt-word-tolerance 只能与 --adapt 一起使用")
	}
	if opts.AdaptGranularity != "" && !validAdaptGranularity(opts.AdaptGranularity) {
		return fmt.Errorf("--adapt-granularity 可选 chapter/arc/free")
	}
	if opts.AdaptRewritePolicy != "" && !validAdaptRewritePolicy(opts.AdaptRewritePolicy) {
		return fmt.Errorf("--adapt-rewrite-policy 可选 full_rewrite/preserve_details")
	}
	if !opts.Headless && !opts.Version && !opts.Update {
		opts.Web = true
		opts.WebHost = web.DefaultHost
		opts.WebPort = web.DefaultPort
		opts.WebOpen = true
	}
	return nil
}

func parseWebOptions(opts *cliOptions, argv []string) error {
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--host":
			if i+1 >= len(argv) {
				return fmt.Errorf("--host 缺少值")
			}
			opts.WebHost = strings.TrimSpace(argv[i+1])
			i++
		case "--port":
			if i+1 >= len(argv) {
				return fmt.Errorf("--port 缺少值")
			}
			port, err := strconv.Atoi(argv[i+1])
			if err != nil || port <= 0 || port > 65535 {
				return fmt.Errorf("--port 必须是 1-65535 的整数")
			}
			opts.WebPort = port
			i++
		case "--runtime-root":
			if i+1 >= len(argv) {
				return fmt.Errorf("--runtime-root 缺少值")
			}
			opts.WebRuntimeRoot = argv[i+1]
			i++
		case "--open":
			opts.WebOpen = true
		case "--config":
			if i+1 >= len(argv) {
				return fmt.Errorf("--config 缺少值")
			}
			opts.ConfigPath = argv[i+1]
			i++
		default:
			return fmt.Errorf("web 不支持参数 %q", argv[i])
		}
	}
	if opts.WebHost == "" {
		return fmt.Errorf("--host 不能为空")
	}
	return nil
}

func validAdaptGranularity(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chapter", "arc", "free":
		return true
	default:
		return false
	}
}

func validAdaptRewritePolicy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "full_rewrite", "preserve_details":
		return true
	default:
		return false
	}
}

func versionInfo() buildversion.Info {
	return buildversion.Resolve(buildversion.Info{Version: version, Commit: commit, Date: date})
}

func runSelfUpdate(target string) error {
	info := versionInfo()
	result, err := buildversion.Update(context.Background(), buildversion.UpdateOptions{
		Repo:           "voocel/ainovel-cli",
		BinaryName:     "ainovel-cli",
		TargetVersion:  target,
		CurrentVersion: info.Version,
	})
	if err != nil {
		return err
	}
	if !result.Updated {
		fmt.Printf("ainovel-cli 已是最新版本 %s\n", result.Version)
		return nil
	}
	fmt.Printf("ainovel-cli 已更新到 %s\n", result.Version)
	fmt.Printf("安装位置：%s\n", result.Path)
	return nil
}

func loadPrompt(opts cliOptions) (string, error) {
	if opts.PromptFile == "" {
		return strings.TrimSpace(opts.Prompt), nil
	}
	var (
		data []byte
		err  error
	)
	if opts.PromptFile == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(opts.PromptFile)
	}
	if err != nil {
		return "", fmt.Errorf("读取 prompt 失败: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
