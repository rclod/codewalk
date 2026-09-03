package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/backends"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/eval"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/service"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// loadWalkthroughTarget accepts a run id, "latest", or a path to a walkthrough
// JSON file, so evaluation works both on stored runs and on exported artifacts.
func loadWalkthroughTarget(env Env, target string) (*walkthrough.Walkthrough, string, error) {
	if target == "" {
		target = "latest"
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, "", err
		}
		w, err := walkthrough.Decode(data)
		return w, "", err
	}
	store, err := openStore()
	if err != nil {
		return nil, "", err
	}
	id, err := store.Resolve(target)
	if err != nil {
		return nil, "", err
	}
	w, err := store.Walkthrough(id)
	return w, id, err
}

// repoForWalkthrough opens the repository a walkthrough describes. Without it,
// evidence-based checks cannot run, so the caller is told rather than left with
// a quietly weaker result.
func repoForWalkthrough(env Env, w *walkthrough.Walkthrough, repoFlag string) *gitrepo.Repo {
	path := firstNonEmpty(resolveRepoPathOptional(env, repoFlag), w.Scope.RepositoryPath, env.Workdir)
	repo, err := gitrepo.Discover(path)
	if err != nil {
		fmt.Fprintf(env.Stderr,
			"warning: could not open the repository this walkthrough describes (%s): %v\n"+
				"         claims will not be verified against source; pass --repo to point at it\n", path, err)
		return nil
	}
	return repo
}

func evalCheck(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval check", "codewalk eval check — deterministic checks on a walkthrough\n\nUsage:\n  codewalk eval check [run-id|latest|walkthrough.json]\n")
	repoFlag := fs.String("repo", "", "Repository the walkthrough describes")
	jsonOut := fs.Bool("json", false, "Emit the result as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	target := "latest"
	if len(rest) > 0 {
		target = rest[0]
	}
	w, runID, err := loadWalkthroughTarget(env, target)
	if err != nil {
		return err
	}
	result, err := eval.Evaluate(ctx, eval.Options{
		Walkthrough: w,
		Repo:        repoForWalkthrough(env, w, *repoFlag),
		Change:      changeSetFromScope(w.Scope),
		Mode:        eval.ModeSmoke,
		RunID:       runID,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSONOut(env, result)
	}
	if err := eval.Report(env.Stdout, result); err != nil {
		return err
	}
	if !result.Passed() {
		return fmt.Errorf("quality gates failed")
	}
	return nil
}

func evalRun(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval run", "codewalk eval run — deterministic and semantic evaluation\n\nUsage:\n  codewalk eval run [run-id|latest|walkthrough.json]\n")
	repoFlag := fs.String("repo", "", "Repository the walkthrough describes")
	mode := fs.String("mode", "standard", "Evaluation mode: smoke, standard or full")
	judgeFlag := fs.String("judges", "", "Comma-separated judge backends (default: from config, else the default backend)")
	understandingPath := fs.String("understanding", "", "Curated understanding model to score coverage against")
	extract := fs.Bool("extract-understanding", false, "Derive an understanding model independently from the repository")
	jsonOut := fs.Bool("json", false, "Emit the result as JSON")
	save := fs.Bool("save", true, "Store the result with the run")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	target := "latest"
	if len(rest) > 0 {
		target = rest[0]
	}
	w, runID, err := loadWalkthroughTarget(env, target)
	if err != nil {
		return err
	}
	evalMode, err := eval.ParseMode(*mode)
	if err != nil {
		return err
	}
	repo := repoForWalkthrough(env, w, *repoFlag)
	root := env.Workdir
	if repo != nil {
		root = repo.Root
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	var judges []agent.Backend
	var extractor agent.Backend
	if evalMode != eval.ModeSmoke {
		// Smoke mode is deterministic only; requiring a judge backend for it
		// would fail runs that need no model at all.
		judges, extractor, err = buildJudges(env, cfg, *judgeFlag, *extract, "")
		if err != nil {
			return err
		}
	}

	opts := eval.Options{
		Walkthrough: w,
		Repo:        repo,
		Change:      changeSetFromScope(w.Scope),
		Mode:        evalMode,
		Judges:      judges,
		Extractor:   extractor,
		RunID:       runID,
	}
	if *understandingPath != "" {
		u, err := eval.LoadUnderstandingModel(*understandingPath)
		if err != nil {
			return err
		}
		opts.Understanding = u
	}

	result, err := eval.Evaluate(ctx, opts)
	if err != nil {
		return err
	}
	if *save && runID != "" {
		store, err := openStore()
		if err == nil {
			_ = store.SaveEval(runID, "eval", result)
		}
	}
	if *jsonOut {
		return writeJSONOut(env, result)
	}
	if err := eval.Report(env.Stdout, result); err != nil {
		return err
	}
	if !result.Passed() {
		return fmt.Errorf("quality gates failed")
	}
	return nil
}

// buildJudges constructs judge backends. Judges are deliberately separate from
// the generation backend so a pipeline cannot grade its own work by default;
// when they end up being the same backend, the caller is told, because the
// result is then a self-assessment rather than an independent evaluation.
func buildJudges(env Env, cfg *config.Config, judgeFlag string, needExtractor bool, generationBackend string) ([]agent.Backend, agent.Backend, error) {
	names := splitList(judgeFlag)
	if len(names) == 0 {
		names = cfg.Eval.Judges
	}
	registry, err := backends.Build(cfg, backends.Options{RequireRoles: []string{"judge"}})
	if err != nil {
		return nil, nil, err
	}
	var judges []agent.Backend
	if len(names) == 0 {
		b, err := registry.For("judge")
		if err != nil {
			return nil, nil, err
		}
		judges = append(judges, b)
	}
	for _, b := range judges {
		if generationBackend != "" && b.Name() == generationBackend {
			fmt.Fprintf(env.Stderr,
				"note: judging with %q, the same backend that generated the walkthrough.\n"+
					"      This is a self-assessment, not an independent evaluation; pass --judges to use another backend.\n",
				b.Name())
		}
	}
	for _, name := range names {
		b, ok := registry.Get(name)
		if !ok {
			return nil, nil, fmt.Errorf("judge backend %q is not configured or is missing credentials", name)
		}
		judges = append(judges, b)
	}
	var extractor agent.Backend
	if needExtractor {
		b, err := registry.For("extractor")
		if err != nil {
			return nil, nil, err
		}
		extractor = b
	}
	return judges, extractor, nil
}

func evalBenchmark(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval benchmark", "codewalk eval benchmark — generate and evaluate one benchmark case\n\nUsage:\n  codewalk eval benchmark <case-dir>\n")
	mode := fs.String("mode", "standard", "Evaluation mode: smoke, standard or full")
	backendFlag := fs.String("backend", "", "Backend used to generate the walkthrough")
	modelFlag := fs.String("model", "", "Model used to generate the walkthrough")
	judgeFlag := fs.String("judges", "", "Comma-separated judge backends")
	keep := fs.Bool("keep", false, "Keep the materialised fixture repository")
	jsonOut := fs.Bool("json", false, "Emit the result as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("eval benchmark needs a case directory")
	}
	c, err := eval.LoadCase(rest[0])
	if err != nil {
		return err
	}
	result, err := runBenchmarkCase(ctx, env, c, benchmarkOptions{
		mode: *mode, backend: *backendFlag, model: *modelFlag, judges: *judgeFlag, keep: *keep,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSONOut(env, result.Result)
	}
	if result.Result != nil {
		if err := eval.Report(env.Stdout, result.Result); err != nil {
			return err
		}
	}
	if !result.Passed {
		return fmt.Errorf("benchmark case %s failed its quality gates", c.ID)
	}
	return nil
}

type benchmarkOptions struct {
	mode    string
	backend string
	model   string
	judges  string
	keep    bool
}

func runBenchmarkCase(ctx context.Context, env Env, c *eval.Case, opts benchmarkOptions) (*eval.CaseResult, error) {
	evalMode, err := eval.ParseMode(opts.mode)
	if err != nil {
		return nil, err
	}

	// Fixtures are materialised into a temporary directory: benchmark runs never
	// write into the source tree.
	workDir, err := os.MkdirTemp("", "codewalk-bench-")
	if err != nil {
		return nil, err
	}
	if !opts.keep {
		defer os.RemoveAll(workDir)
	} else {
		fmt.Fprintf(env.Stderr, "fixture kept at %s\n", workDir)
	}

	repoPath, err := c.Materialize(workDir)
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(repoPath)
	if err != nil {
		return nil, err
	}
	svc, err := newService()
	if err != nil {
		return nil, err
	}

	sel := gitrepo.Selection{Mode: gitrepo.SelectorMode(orDefaultString(c.Selector, string(gitrepo.ModeAuto))), Base: c.Base, Head: c.Head}
	if c.Base != "" && c.Head != "" {
		sel.Mode = gitrepo.ModeRange
		sel.Spec = c.Base + ".." + c.Head
	}

	progress := newProgressReporter(env.Stderr)
	gen, err := svc.Generate(ctx, service.GenerateRequest{
		RepositoryPath:  repoPath,
		Kind:            c.WalkthroughKind(),
		Selection:       sel,
		Focus:           c.Focus,
		Subtree:         c.Subtree,
		BackendOverride: opts.backend,
		ModelOverride:   opts.model,
		Config:          cfg,
		Observer:        progress,
	})
	progress.stop()
	if err != nil {
		return &eval.CaseResult{CaseID: c.ID, Error: err.Error()}, nil
	}

	var judges []agent.Backend
	var extractor agent.Backend
	if evalMode != eval.ModeSmoke {
		judges, extractor, err = buildJudges(env, cfg, opts.judges, false, opts.backend)
		if err != nil {
			return nil, err
		}
	}
	repo, err := gitrepo.Discover(repoPath)
	if err != nil {
		return nil, err
	}
	change, _ := repo.BuildChangeSet(ctx, sel)

	result, err := eval.Evaluate(ctx, eval.Options{
		Walkthrough:          gen.Walkthrough,
		Repo:                 repo,
		Change:               change,
		Mode:                 evalMode,
		Judges:               judges,
		Extractor:            extractor,
		Understanding:        c.Understanding,
		RunID:                gen.Run.ID,
		CaseID:               c.ID,
		GenerationDurationMS: gen.Run.Metrics.DurationMS,
		GenerationTokens:     gen.Run.Metrics.Usage.InputTokens + gen.Run.Metrics.Usage.OutputTokens,
		GenerationModelCalls: gen.Run.Metrics.Usage.Calls,
	})
	if err != nil {
		return &eval.CaseResult{CaseID: c.ID, RunID: gen.Run.ID, Error: err.Error()}, nil
	}
	if store, err := openStore(); err == nil {
		_ = store.SaveEval(gen.Run.ID, "eval", result)
	}
	return &eval.CaseResult{
		CaseID: c.ID,
		RunID:  gen.Run.ID,
		Passed: result.Passed(),
		Score:  result.Average(),
		Result: result,
	}, nil
}

func evalSuite(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval suite", "codewalk eval suite — run a benchmark corpus\n\nUsage:\n  codewalk eval suite <corpus-dir>\n")
	mode := fs.String("mode", "standard", "Evaluation mode: smoke, standard or full")
	backendFlag := fs.String("backend", "", "Backend used to generate walkthroughs")
	modelFlag := fs.String("model", "", "Model used to generate walkthroughs")
	judgeFlag := fs.String("judges", "", "Comma-separated judge backends")
	tag := fs.String("tag", "", "Only run cases with this tag")
	outPath := fs.String("out", "", "Write the full suite result as JSON to this path")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	dir := "benchmarks/cases"
	if len(rest) > 0 {
		dir = rest[0]
	}
	cases, err := eval.LoadCorpus(dir)
	if err != nil {
		return err
	}
	suite := &eval.SuiteReport{}
	for _, c := range cases {
		if *tag != "" && !hasTag(c.Tags, *tag) {
			continue
		}
		fmt.Fprintf(env.Stderr, "\n=== %s: %s\n", c.ID, c.Description)
		res, err := runBenchmarkCase(ctx, env, c, benchmarkOptions{
			mode: *mode, backend: *backendFlag, model: *modelFlag, judges: *judgeFlag,
		})
		if err != nil {
			suite.Cases = append(suite.Cases, eval.CaseResult{CaseID: c.ID, Error: err.Error()})
			continue
		}
		suite.Cases = append(suite.Cases, *res)
	}
	fmt.Fprintln(env.Stdout)
	if err := eval.WriteSuite(env.Stdout, suite); err != nil {
		return err
	}
	if *outPath != "" {
		data, err := json.MarshalIndent(suite, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*outPath, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(env.Stderr, "wrote %s\n", *outPath)
	}
	for _, c := range suite.Cases {
		if !c.Passed {
			return fmt.Errorf("%d of %d benchmark cases failed", countFailed(suite), len(suite.Cases))
		}
	}
	return nil
}

func countFailed(s *eval.SuiteReport) int {
	n := 0
	for _, c := range s.Cases {
		if !c.Passed {
			n++
		}
	}
	return n
}

func evalCompare(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval compare", "codewalk eval compare — blind pairwise comparison\n\nUsage:\n  codewalk eval compare <run-a|file-a> <run-b|file-b>\n")
	repoFlag := fs.String("repo", "", "Repository the walkthroughs describe")
	judgeFlag := fs.String("judges", "", "Judge backend")
	labelA := fs.String("label-a", "A", "Label for the first candidate")
	labelB := fs.String("label-b", "B", "Label for the second candidate")
	jsonOut := fs.Bool("json", false, "Emit the comparison as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("eval compare needs two walkthroughs")
	}
	wa, _, err := loadWalkthroughTarget(env, rest[0])
	if err != nil {
		return err
	}
	wb, _, err := loadWalkthroughTarget(env, rest[1])
	if err != nil {
		return err
	}
	repo := repoForWalkthrough(env, wa, *repoFlag)
	root := env.Workdir
	if repo != nil {
		root = repo.Root
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	judges, _, err := buildJudges(env, cfg, *judgeFlag, false, "")
	if err != nil {
		return err
	}
	repoTools := toolsForRepo(repo, wa)

	cmp, err := eval.ComparePair(ctx, judges[0], repoTools,
		eval.Candidate{Label: *labelA, Walkthrough: wa},
		eval.Candidate{Label: *labelB, Walkthrough: wb},
		rand.New(rand.NewSource(time.Now().UnixNano())),
	)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSONOut(env, cmp)
	}
	fmt.Fprintf(env.Stdout, "Blind comparison: %s vs %s\n\n", cmp.A, cmp.B)
	for _, dim := range eval.AllDimensions {
		if winner, ok := cmp.Dimensions[dim]; ok {
			fmt.Fprintf(env.Stdout, "  %-24s %s\n", dim, winner)
		}
	}
	fmt.Fprintf(env.Stdout, "\n  %-24s %s\n", "overall", cmp.Overall)
	if cmp.OverallReason != "" {
		fmt.Fprintf(env.Stdout, "\n%s\n", cmp.OverallReason)
	}
	return nil
}

func evalReport(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet(env, "eval report", "codewalk eval report — show a stored evaluation\n\nUsage:\n  codewalk eval report <run-id|latest>\n")
	jsonOut := fs.Bool("json", false, "Emit the stored result as JSON")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	target := "latest"
	if len(rest) > 0 {
		target = rest[0]
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	var result eval.Result
	if err := store.LoadEval(target, "eval", &result); err != nil {
		return fmt.Errorf("no stored evaluation for %s: run `codewalk eval run %s` first", target, target)
	}
	if *jsonOut {
		return writeJSONOut(env, &result)
	}
	return eval.Report(env.Stdout, &result)
}

// toolsForRepo builds the read-only tool surface a judge uses to verify claims
// against the repository. It returns nil when no repository is available, in
// which case the judge scores what it can from the walkthrough alone.
func toolsForRepo(repo *gitrepo.Repo, w *walkthrough.Walkthrough) *tools.Repo {
	if repo == nil {
		return nil
	}
	return tools.NewRepo(repo, changeSetFromScope(w.Scope), nil, tools.Options{AllowGitHistory: true})
}

func writeJSONOut(env Env, v any) error {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// splitList parses a comma-separated flag value.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func orDefaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
