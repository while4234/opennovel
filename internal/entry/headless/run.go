package headless

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Options struct {
	Prompt             string
	Answers            AnswersFile
	AdaptPath          string
	AdaptGranularity   string
	AdaptRewritePolicy string
	AdaptWordTolerance float64
	Stdout             io.Writer
	Stderr             io.Writer
}

// Run 以无界面模式运行会话内核，直接消费 Engine 事件与流式输出。
// 未来若新增“续写已有小说”等共享启动方式，不应直接堆到这里，
// 而应先落到 internal/entry/startup，再由 headless 入口调用。
func Run(cfg bootstrap.Config, bundle assets.Bundle, opts Options) error {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	eng, err := host.New(cfg, bundle)
	if err != nil {
		return err
	}
	answerHandler := newAnswerFileHandler(opts.Answers, func() { eng.Abort() })
	eng.AskUser().SetHandler(answerHandler.handle)
	cleanup := logger.SetupFile(eng.Dir(), "headless.log", false)
	defer cleanup()
	defer eng.Close()
	// 运行结束 / 出错返回时落一份脱敏诊断，方便 headless 用户贴 issue。
	// （外部 kill 的挂死不走 defer，仍需在 TUI 里手动 /diag。）
	defer func() { _, _ = diag.Export(store.NewStore(eng.Dir())) }()

	prompt := strings.TrimSpace(opts.Prompt)
	if opts.AdaptPath != "" {
		if prompt == "" {
			return fmt.Errorf("headless 改编模式需要 --prompt 或 --prompt-file 作为改编 brief")
		}
		granularity := opts.AdaptGranularity
		if strings.TrimSpace(granularity) == "" {
			granularity = startup.DefaultAdaptationGranularity
		}
		rewritePolicy := opts.AdaptRewritePolicy
		if strings.TrimSpace(rewritePolicy) == "" {
			rewritePolicy = startup.DefaultAdaptationRewritePolicy
		}
		wordTolerance := startup.AdaptationWordToleranceForGranularity(granularity, opts.AdaptWordTolerance)
		plan, err := startup.PrepareAdaptNovel(startup.Request{
			Mode:               startup.ModeAdaptNovel,
			UserPrompt:         prompt,
			NovelPath:          opts.AdaptPath,
			OutputDir:          eng.Dir(),
			Interactive:        false,
			AdaptGranularity:   granularity,
			AdaptRewritePolicy: rewritePolicy,
			AdaptWordTolerance: wordTolerance,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "headless 改编启动: %s (granularity=%s rewrite_policy=%s word_tolerance=%s)\n",
			eng.Dir(),
			plan.AdaptGranularity,
			plan.AdaptRewritePolicy,
			startup.FormatAdaptationWordTolerance(plan.AdaptGranularity, plan.AdaptWordTolerance))
		if err := runAdaptPreparation(context.Background(), eng, opts.AdaptPath, stderr); err != nil {
			return err
		}
		if err := eng.PrepareExternalSourceUserRules(plan.RawPrompt); err != nil {
			return err
		}
		if err := eng.StartAdaptationPreparedWithOptions(adapt.ProposalOptions{
			Brief:         plan.RawPrompt,
			SourcePath:    opts.AdaptPath,
			Granularity:   plan.AdaptGranularity,
			RewritePolicy: plan.AdaptRewritePolicy,
			WordTolerance: plan.AdaptWordTolerance,
		}); err != nil {
			return err
		}
	} else if prompt != "" {
		plan, err := startup.PrepareQuick(startup.Request{
			Mode:        startup.ModeQuick,
			UserPrompt:  prompt,
			OutputDir:   eng.Dir(),
			Interactive: true,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "headless 启动: %s\n", eng.Dir())
		// 启动侧确定性生成本书用户规则快照（用原始 prompt 归一化），须在 StartPrepared 前。
		if err := eng.PrepareUserRules(plan.RawPrompt); err != nil {
			return err
		}
		if err := eng.SetWordBudget(plan.WordBudget); err != nil {
			return err
		}
		if err := eng.StartPrepared(plan.StartPrompt); err != nil {
			return err
		}
	} else {
		items, err := eng.ReplayQueue(0)
		if err != nil {
			return err
		}
		roundHasContent, err := replayQueue(items, stdout, stderr)
		if err != nil {
			return err
		}
		label, err := eng.Resume()
		if err != nil {
			return err
		}
		if label == "" {
			return fmt.Errorf("headless 模式需要 --prompt，或输出目录 %q 下已有可恢复会话", eng.Dir())
		}
		fmt.Fprintf(stderr, "headless 恢复: %s (%s)\n", eng.Dir(), label)
		return consumeWithAnswerCheck(eng, answerHandler, stdout, stderr, roundHasContent)
	}

	return consumeWithAnswerCheck(eng, answerHandler, stdout, stderr, false)
}

func consumeWithAnswerCheck(eng *host.Host, answers *answerFileHandler, stdout, stderr io.Writer, roundHasContent bool) error {
	err := consume(eng, stdout, stderr, roundHasContent)
	if answerErr := answers.Err(); answerErr != nil {
		return answerErr
	}
	return err
}

func runAdaptPreparation(ctx context.Context, eng *host.Host, sourcePath string, stderr io.Writer) error {
	ch, err := eng.PrepareAdaptationSource(ctx, sourcePath)
	if err != nil {
		return err
	}
	for ev := range ch {
		writeAdaptEvent(stderr, ev)
		if ev.Stage == adapt.StageError {
			if ev.Err != nil {
				return ev.Err
			}
			return fmt.Errorf("%s", ev.Message)
		}
	}
	return nil
}

func consume(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return nil
			}
			writeEvent(stderr, ev)
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta == "" {
				continue
			}
			if _, err := io.WriteString(stdout, delta); err != nil {
				return err
			}
			roundHasContent = true
		case _, ok := <-eng.Done():
			if !ok {
				return nil
			}
			return drainPending(eng, stdout, stderr, roundHasContent)
		}
	}
}

func drainPending(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if ok {
				writeEvent(stderr, ev)
			}
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta != "" {
				if _, err := io.WriteString(stdout, delta); err != nil {
					return err
				}
				roundHasContent = true
			}
		default:
			if roundHasContent {
				if _, err := io.WriteString(stdout, "\n"); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func writeEvent(w io.Writer, ev host.Event) {
	if w == nil || strings.TrimSpace(ev.Summary) == "" {
		return
	}
	ts := ev.Time.Format("15:04:05")
	if ts == "00:00:00" {
		ts = "--:--:--"
	}
	fmt.Fprintf(w, "[%s] [%s] %s\n", ts, ev.Category, ev.Summary)
}

func writeAdaptEvent(w io.Writer, ev adapt.Event) {
	if w == nil || strings.TrimSpace(ev.Message) == "" {
		return
	}
	ts := ev.Time.Format("15:04:05")
	if ts == "00:00:00" {
		ts = "--:--:--"
	}
	msg := ev.Message
	if ev.Total > 0 && ev.Current > 0 {
		msg = fmt.Sprintf("%s (%d/%d)", msg, ev.Current, ev.Total)
	}
	if ev.Err != nil {
		msg += ": " + ev.Err.Error()
	}
	fmt.Fprintf(w, "[%s] [ADAPT:%s] %s\n", ts, ev.Stage, msg)
}

func replayQueue(items []domain.RuntimeQueueItem, stdout, stderr io.Writer) (bool, error) {
	var roundHasContent bool
	for _, item := range items {
		switch item.Kind {
		case domain.RuntimeQueueUIEvent:
			writeEvent(stderr, host.Event{
				Time:     item.Time,
				Category: item.Category,
				Summary:  item.Summary,
			})
		case domain.RuntimeQueueStreamClear:
			if roundHasContent {
				if _, err := io.WriteString(stdout, "\n\n"); err != nil {
					return roundHasContent, err
				}
				roundHasContent = false
			}
		case domain.RuntimeQueueStreamDelta:
			text := host.ReplayDeltaText(item)
			if text == "" {
				continue
			}
			if _, err := io.WriteString(stdout, text); err != nil {
				return roundHasContent, err
			}
			roundHasContent = true
		}
	}
	return roundHasContent, nil
}
