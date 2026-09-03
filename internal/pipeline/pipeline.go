// Package pipeline turns a repository and a change into a walkthrough.
//
// The pipeline is multi-stage by design. One model call asked to investigate,
// decide what matters, choose a teaching order and write well tends to do all
// four adequately and none of them carefully. Separating the responsibilities
// makes each one improvable, measurable and independently configurable:
//
//	Investigator -> Mental Model -> Planner -> Author -> Clarity Editor -> Grounding
//
// Stages are skipped when they cannot pay for themselves; a one-line
// configuration change does not need a planning stage. Every stage records what
// ran, on which backend, with which prompt version.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/agent"
	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/jsonx"
	"github.com/rclod/codewalk/internal/llm"
	"github.com/rclod/codewalk/internal/refcheck"
	"github.com/rclod/codewalk/internal/repomap"
	"github.com/rclod/codewalk/internal/tools"
	"github.com/rclod/codewalk/internal/walkthrough"
)

// Options configures one pipeline run.
type Options struct {
	Repo     *gitrepo.Repo
	Change   *gitrepo.ChangeSet // nil for a codebase walkthrough
	Kind     walkthrough.Kind
	Config   *config.Config
	Registry *agent.Registry
	Observer agent.Observer

	// Depth is auto, brief, standard or deep.
	Depth string
	// Focus is an optional user instruction, for example "focus on the
	// persistence changes" or "I don't know this codebase at all".
	Focus string
	// Subtree limits a codebase walkthrough to part of the repository.
	Subtree string
	// RunID is recorded in the walkthrough metadata.
	RunID string
	// Version is the codewalk version string recorded as the generator.
	Version string
}

// Result is a completed pipeline run.
type Result struct {
	Walkthrough *walkthrough.Walkthrough
	Artifacts   Artifacts
	Stages      []StageRecord
	Usage       llm.Usage
	ToolCalls   []tools.Invocation
	RepoMap     *repomap.Map
	DurationMS  int64
}

// runner holds per-run state shared by the stages.
type runner struct {
	opts    Options
	repo    *tools.Repo
	rmap    *repomap.Map
	result  *Result
	started time.Time

	// authorComplexity records the complexity the author supplied before
	// normalisation, so finalisation can tell "the author did not say" from
	// "the author said level 1".
	authorComplexity int
}

// Run executes the pipeline.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Repo == nil {
		return nil, fmt.Errorf("pipeline: no repository")
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("pipeline: no agent backends configured")
	}
	if opts.Kind == "" {
		opts.Kind = walkthrough.KindChange
	}
	if opts.Kind == walkthrough.KindChange && opts.Change == nil {
		return nil, fmt.Errorf("pipeline: a change walkthrough needs a change set")
	}

	started := time.Now()
	headRev := ""
	if opts.Change != nil {
		headRev = opts.Change.HeadRev()
	}
	rmap, err := buildRepoMap(ctx, opts, mapRev(headRev))
	if err != nil {
		return nil, fmt.Errorf("build repository map: %w", err)
	}

	repoTools := tools.NewRepo(opts.Repo, opts.Change, rmap, tools.Options{
		MaxFileBytes:    opts.Config.Analysis.MaxFileBytes,
		MaxDiffBytes:    opts.Config.Analysis.MaxDiffBytesPerFile,
		AllowGitHistory: opts.Config.Analysis.GitHistory,
	})

	r := &runner{
		opts:    opts,
		repo:    repoTools,
		rmap:    rmap,
		started: started,
		result: &Result{
			Artifacts: Artifacts{},
			RepoMap:   rmap,
		},
	}

	w, err := r.run(ctx)
	r.result.ToolCalls = repoTools.Invocations()
	r.result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		return r.result, err
	}
	r.result.Walkthrough = w
	return r.result, nil
}

// buildRepoMap computes the repository map, reusing a cached one when the
// revision is immutable. The map is a deterministic function of the tracked
// file list, so this is safe and saves the same work on every walkthrough of a
// commit.
func buildRepoMap(ctx context.Context, opts Options, rev string) (*repomap.Map, error) {
	var cache *repomap.Cache
	if opts.Config.Cache.Enabled {
		cache = repomap.NewCache(opts.Config.Cache.Dir, time.Duration(opts.Config.Cache.TTLHours)*time.Hour)
		if cached := cache.Load(opts.Repo.Root, rev); cached != nil {
			return cached, nil
		}
	}
	m, err := repomap.Build(ctx, opts.Repo, rev)
	if err != nil {
		return nil, err
	}
	cache.Store(opts.Repo.Root, rev, m)
	return m, nil
}

func mapRev(headRev string) string {
	if headRev == gitrepo.WorkingTree {
		return ""
	}
	return headRev
}

func (r *runner) run(ctx context.Context) (*walkthrough.Walkthrough, error) {
	evidence, err := r.investigate(ctx)
	if err != nil {
		return nil, err
	}
	model, err := r.buildMentalModel(ctx, evidence)
	if err != nil {
		return nil, err
	}
	plan, err := r.plan(ctx, model)
	if err != nil {
		return nil, err
	}
	w, err := r.author(ctx, model, plan)
	if err != nil {
		return nil, err
	}
	w = r.edit(ctx, w, model)
	w = r.ground(ctx, w)
	r.finalize(w, model, evidence)
	return w, nil
}

// stageBackend resolves the backend for a role and emits a stage-start event.
func (r *runner) stageBackend(role string) (agent.Backend, error) {
	b, err := r.opts.Registry.For(role)
	if err != nil {
		return nil, err
	}
	r.emit(agent.Event{Kind: agent.EventStageStart, Role: role, Detail: b.Descriptor()})
	return b, nil
}

func (r *runner) emit(e agent.Event) {
	if r.opts.Observer != nil {
		e.Elapsed = time.Since(r.started).Milliseconds()
		r.opts.Observer.OnEvent(e)
	}
}

// runJSONStage executes a stage that must return JSON, retrying once with the
// parse error fed back. Models occasionally emit prose around JSON; a single
// cheap repair is far better than failing a whole run.
func (r *runner) runJSONStage(ctx context.Context, role, promptVersion string, b agent.Backend, task agent.Task, out any) (*StageRecord, error) {
	task.Role = role
	task.ExpectJSON = true

	rec := &StageRecord{
		Name:          role,
		Backend:       b.Name(),
		BackendKind:   b.Kind(),
		PromptVersion: promptVersion,
	}
	basePrompt := task.Prompt
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			rec.Repairs++
			task.Prompt = basePrompt +
				"\n\nYour previous response could not be parsed as JSON: " + lastErr.Error() +
				"\nReturn only the JSON object, with no fences and no commentary."
		}
		res, err := b.Execute(ctx, task, r.repo)
		if err != nil {
			rec.Error = err.Error()
			return rec, err
		}
		rec.Model = res.Model
		rec.DurationMS += res.DurationMS
		rec.Usage.Add(res.Usage)
		rec.Steps += res.Steps
		rec.ToolCalls += res.ToolCalls
		rec.Truncated = rec.Truncated || res.Truncated

		if err := jsonx.Unmarshal(res.Text, out); err != nil {
			lastErr = err
			continue
		}
		return rec, nil
	}
	rec.Error = fmt.Sprintf("could not parse JSON output: %v", lastErr)
	return rec, fmt.Errorf("%s stage returned unparseable output: %w", role, lastErr)
}

func (r *runner) record(rec *StageRecord) {
	if rec == nil {
		return
	}
	r.result.Stages = append(r.result.Stages, *rec)
	r.result.Usage.Add(rec.Usage)
	r.emit(agent.Event{Kind: agent.EventStageEnd, Role: rec.Name, Detail: rec.Error})
}

func (r *runner) skip(role, reason string) {
	r.result.Stages = append(r.result.Stages, StageRecord{Name: role, Skipped: reason})
	r.emit(agent.Event{Kind: agent.EventNote, Role: role, Detail: "skipped: " + reason})
}

// ---------------------------------------------------------------------------
// Stage 1: Investigator

func (r *runner) investigate(ctx context.Context) (*EvidenceReport, error) {
	b, err := r.stageBackend("investigator")
	if err != nil {
		return nil, err
	}
	agentCfg := r.opts.Config.ForRole("investigator")

	prompt := &strings.Builder{}
	prompt.WriteString(r.contextBriefing(ctx))
	if r.opts.Kind == walkthrough.KindChange {
		prompt.WriteString("\n\nInvestigate this change. Establish what it does, which components participate, what behaviour existed before, what behaviour exists now, and what a reader would need to understand it. Read unchanged code wherever that is required.")
	} else {
		prompt.WriteString("\n\nInvestigate this repository. Establish what it is, how it runs, its major subsystems, its important execution paths, where state lives, what it integrates with, and what runs asynchronously. Discover the architecture from the code rather than from the directory layout.")
	}
	if r.opts.Focus != "" {
		fmt.Fprintf(prompt, "\n\nThe reader asked specifically: %s\nLet that shape what you prioritise, without ignoring what they would need to understand the answer.", r.opts.Focus)
	}
	if b.Capabilities().OwnTools {
		prompt.WriteString("\n\nUse your own file, search and git tools to read the repository. It is checked out in your working directory.")
	}

	var report EvidenceReport
	task := agent.Task{
		System:          investigatorSystem,
		Prompt:          prompt.String(),
		SchemaHint:      investigatorSchema,
		MaxSteps:        agentCfg.MaxSteps,
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "investigator", PromptVersionInvestigator, b, task, &report)
	r.record(rec)
	if err != nil {
		return nil, err
	}
	report.FilesInspected = r.repo.FilesInspected()
	r.result.Artifacts.set("evidence", report)
	return &report, nil
}

// ---------------------------------------------------------------------------
// Stage 2: Mental model

func (r *runner) buildMentalModel(ctx context.Context, evidence *EvidenceReport) (*MentalModel, error) {
	b, err := r.stageBackend("mental_model")
	if err != nil {
		return nil, err
	}
	agentCfg := r.opts.Config.ForRole("mental_model")

	evidenceJSON, _ := json.MarshalIndent(evidence, "", "  ")
	prompt := &strings.Builder{}
	prompt.WriteString(r.contextBriefing(ctx))
	prompt.WriteString("\n\nThe investigator gathered this evidence:\n\n<evidence>\n")
	prompt.Write(evidenceJSON)
	prompt.WriteString("\n</evidence>\n\nDecide what a human actually needs to understand. Organise it around concepts and components, not files. Verify anything you are unsure of with the tools before relying on it.")
	if r.opts.Focus != "" {
		fmt.Fprintf(prompt, "\n\nThe reader asked specifically: %s", r.opts.Focus)
	}
	if depth := r.depthGuidance(); depth != "" {
		prompt.WriteString("\n\n" + depth)
	}

	var model MentalModel
	task := agent.Task{
		System:          mentalModelSystem,
		Prompt:          prompt.String(),
		SchemaHint:      mentalModelSchema,
		MaxSteps:        agentCfg.MaxSteps / 2,
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "mental_model", PromptVersionMentalModel, b, task, &model)
	r.record(rec)
	if err != nil {
		return nil, err
	}
	if model.Complexity.Level == 0 {
		model.Complexity = evidence.Complexity
	}
	r.result.Artifacts.set("mental_model", model)
	return &model, nil
}

// ---------------------------------------------------------------------------
// Stage 3: Planner

func (r *runner) plan(ctx context.Context, model *MentalModel) (*Plan, error) {
	level := model.Complexity.Level
	if level <= 1 && r.opts.Depth != "deep" {
		// A trivial change does not need a planning stage: the author can hold
		// the whole shape in one step. Skipping it saves a model call and, more
		// importantly, avoids inflating a one-line change into a report.
		r.skip("planner", "complexity level 1 does not need a separate teaching plan")
		return trivialPlan(model), nil
	}
	if !r.opts.Config.RoleEnabled("planner") {
		r.skip("planner", "disabled by configuration")
		return trivialPlan(model), nil
	}

	b, err := r.stageBackend("planner")
	if err != nil {
		return nil, err
	}
	agentCfg := r.opts.Config.ForRole("planner")

	modelJSON, _ := json.MarshalIndent(model, "", "  ")
	prompt := &strings.Builder{}
	fmt.Fprintf(prompt, "%s\n\nThis is the mental model a reader needs:\n\n<mental_model>\n%s\n</mental_model>\n\nPlan how to teach it.",
		r.scopeBriefing(), modelJSON)
	if r.opts.Focus != "" {
		fmt.Fprintf(prompt, "\n\nThe reader asked specifically: %s\nPlan an explanation that answers that, including the context they need for the answer to make sense.", r.opts.Focus)
	}
	if depth := r.depthGuidance(); depth != "" {
		prompt.WriteString("\n\n" + depth)
	}

	var plan Plan
	task := agent.Task{
		System:          plannerSystem,
		Prompt:          prompt.String(),
		SchemaHint:      plannerSchema,
		MaxSteps:        4,
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "planner", PromptVersionPlanner, b, task, &plan)
	r.record(rec)
	if err != nil {
		return nil, err
	}
	if len(plan.Steps) == 0 {
		plan = *trivialPlan(model)
	}
	r.result.Artifacts.set("plan", plan)
	return &plan, nil
}

// trivialPlan is the fallback shape for changes too small to plan. The budget is
// explicit because "short" is not self-enforcing: without a number, a capable
// model will happily produce five careful paragraphs about a one-line change.
func trivialPlan(model *MentalModel) *Plan {
	p := &Plan{
		ComplexityLevel: model.Complexity.Level,
		TargetShape: "One step, roughly 100-150 words. State what changed, what it means in practice, " +
			"and anything genuinely non-obvious about it. Include at most one concept, and only if the " +
			"change cannot be understood without it. No diagram, no deep dive, no architecture section.",
	}
	p.Steps = append(p.Steps, PlannedStep{
		Title: "What this changes",
		Kind:  "outcome",
		Goal:  "Understand the change and its practical effect.",
	})
	return p
}

// sizeBudget states how much the reader should have to read, in proportion to
// how much context the subject actually needs. Depth calibration is the
// dimension that degrades most quietly, because over-explaining looks like
// thoroughness.
func sizeBudget(level int) string {
	switch level {
	case 1:
		return "This is a trivial change. The whole walkthrough should be readable in under a minute: " +
			"one step, roughly 100-150 words, at most one concept, no diagram. If you find yourself " +
			"explaining the surrounding architecture, you have overshot the change."
	case 2:
		return "This is a local change. Two or three short steps, a few hundred words in total. " +
			"Explain the component that changed and its immediate effect, not the wider system."
	case 3:
		return "This change spans several components. Three to five steps, usually including one flow. " +
			"Include architecture only where it is needed to follow the flow."
	case 4:
		return "This is an architectural change. Five to seven steps, a before/after comparison, and " +
			"at most one or two diagrams where they genuinely lower the effort of understanding."
	case 5:
		return "This is a systemic change. Six to nine steps. Lead with the shape of the system, then " +
			"the paths through it. Even here, every step must earn its place."
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Stage 4: Author

func (r *runner) author(ctx context.Context, model *MentalModel, plan *Plan) (*walkthrough.Walkthrough, error) {
	b, err := r.stageBackend("author")
	if err != nil {
		return nil, err
	}
	agentCfg := r.opts.Config.ForRole("author")

	modelJSON, _ := json.MarshalIndent(model, "", "  ")
	planJSON, _ := json.MarshalIndent(plan, "", "  ")
	prompt := &strings.Builder{}
	fmt.Fprintf(prompt, "%s\n\n<mental_model>\n%s\n</mental_model>\n\n<plan>\n%s\n</plan>\n\nWrite the walkthrough.",
		r.scopeBriefing(), modelJSON, planJSON)
	if budget := sizeBudget(model.Complexity.Level); budget != "" {
		fmt.Fprintf(prompt, "\n\n%s", budget)
	}
	prompt.WriteString("\n\nUse the tools to confirm exact paths, symbols and line ranges for the code references you include. A reference a reader cannot follow is worse than no reference.")
	if r.opts.Focus != "" {
		fmt.Fprintf(prompt, "\n\nThe reader asked specifically: %s", r.opts.Focus)
	}

	var authored walkthrough.Walkthrough
	task := agent.Task{
		System:          authorSystem,
		Prompt:          prompt.String(),
		SchemaHint:      authorSchemaHint,
		MaxSteps:        maxInt(6, agentCfg.MaxSteps/2),
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "author", PromptVersionAuthor, b, task, &authored)
	r.record(rec)
	if err != nil {
		return nil, err
	}
	r.authorComplexity = authored.Complexity.Level
	authored.Normalize()
	r.result.Artifacts.set("authored_walkthrough", authored)
	return &authored, nil
}

// ---------------------------------------------------------------------------
// Stage 5: Clarity editor

func (r *runner) edit(ctx context.Context, w *walkthrough.Walkthrough, model *MentalModel) *walkthrough.Walkthrough {
	if !r.opts.Config.RoleEnabled("editor") {
		r.skip("editor", "disabled by configuration")
		return w
	}
	b, err := r.stageBackend("editor")
	if err != nil {
		r.skip("editor", err.Error())
		return w
	}
	agentCfg := r.opts.Config.ForRole("editor")

	wJSON, _ := json.MarshalIndent(w, "", "  ")
	prompt := fmt.Sprintf("%s\n\nThe reader needs to end up understanding:\n%s\n\n<walkthrough>\n%s\n</walkthrough>\n\n%s\n\nEdit it for comprehension and return the complete revised walkthrough.",
		r.scopeBriefing(), bulletList(model.MustUnderstand), wJSON, sizeBudget(model.Complexity.Level))

	var out EditorResult
	task := agent.Task{
		System:          editorSystem,
		Prompt:          prompt,
		SchemaHint:      `{"walkthrough": ` + authorSchemaHint + `, "edits": ["what you changed and why"]}`,
		MaxSteps:        3,
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "editor", PromptVersionEditor, b, task, &out)
	r.record(rec)
	if err != nil {
		// An editing failure must not lose a good walkthrough; keep the
		// authored version and record why the stage did not apply.
		return w
	}
	if len(out.Walkthrough.Steps) == 0 {
		r.result.Stages[len(r.result.Stages)-1].Error = "editor returned no steps; keeping the authored walkthrough"
		return w
	}
	edited := out.Walkthrough
	if edited.Complexity.Level != 0 {
		r.authorComplexity = edited.Complexity.Level
	}
	edited.Normalize()
	r.result.Artifacts.set("editor", out)
	return &edited
}

// ---------------------------------------------------------------------------
// Stage 6: Grounding

func (r *runner) ground(ctx context.Context, w *walkthrough.Walkthrough) *walkthrough.Walkthrough {
	headRev, baseRev := r.revisions()
	checker := refcheck.New(r.opts.Repo, headRev, baseRev)

	report := &GroundingReport{Verdict: "grounded"}
	// Deterministic first: a file system answers reference questions faster and
	// more reliably than a model.
	for _, issue := range checker.Prune(ctx, w) {
		report.PrunedRefs = append(report.PrunedRefs, issue.String())
	}

	if !r.opts.Config.RoleEnabled("grounding") {
		r.skip("grounding", "semantic grounding disabled by configuration")
		r.result.Artifacts.set("grounding", report)
		return w
	}
	b, err := r.stageBackend("grounding")
	if err != nil {
		r.skip("grounding", err.Error())
		r.result.Artifacts.set("grounding", report)
		return w
	}
	agentCfg := r.opts.Config.ForRole("grounding")

	wJSON, _ := json.MarshalIndent(w, "", "  ")
	prompt := fmt.Sprintf("%s\n\n<walkthrough>\n%s\n</walkthrough>\n\nVerify this walkthrough against the repository.", r.scopeBriefing(), wJSON)

	var modelReport GroundingReport
	task := agent.Task{
		System:          groundingSystem,
		Prompt:          prompt,
		SchemaHint:      groundingSchema,
		MaxSteps:        maxInt(8, agentCfg.MaxSteps/2),
		Model:           agentCfg.Model,
		ReasoningEffort: agentCfg.ReasoningEffort,
	}
	rec, err := r.runJSONStage(ctx, "grounding", PromptVersionGrounding, b, task, &modelReport)
	r.record(rec)
	if err != nil {
		r.result.Artifacts.set("grounding", report)
		return w
	}
	modelReport.PrunedRefs = report.PrunedRefs
	r.result.Artifacts.set("grounding", modelReport)

	if len(modelReport.Contradicted) > 0 {
		w = r.correct(ctx, w, &modelReport)
	}
	// Unverifiable claims become explicit uncertainty rather than silent risk.
	for _, u := range modelReport.Unsupported {
		w.Uncertainties = append(w.Uncertainties, walkthrough.Uncertainty{
			Question: u.Claim,
			Known:    u.Suggestion,
			Unknown:  u.Why,
		})
	}
	return w
}

// correct applies narrow fixes for contradicted claims.
func (r *runner) correct(ctx context.Context, w *walkthrough.Walkthrough, report *GroundingReport) *walkthrough.Walkthrough {
	b, err := r.stageBackend("author")
	if err != nil {
		return w
	}
	affected := map[string]bool{}
	for _, c := range report.Contradicted {
		affected[c.StepID] = true
	}
	var steps []walkthrough.Step
	for _, s := range w.Steps {
		if affected[s.ID] {
			steps = append(steps, s)
		}
	}
	if len(steps) == 0 {
		return w
	}
	stepsJSON, _ := json.MarshalIndent(steps, "", "  ")
	issuesJSON, _ := json.MarshalIndent(report.Contradicted, "", "  ")
	prompt := fmt.Sprintf("%s\n\n<steps>\n%s\n</steps>\n\n<verified_problems>\n%s\n</verified_problems>\n\nRewrite only these steps so they match the evidence.",
		r.scopeBriefing(), stepsJSON, issuesJSON)

	var out CorrectionResult
	task := agent.Task{
		System:     correctionSystem,
		Prompt:     prompt,
		SchemaHint: correctionSchema,
		MaxSteps:   4,
	}
	rec, err := r.runJSONStage(ctx, "correction", PromptVersionCorrection, b, task, &out)
	r.record(rec)
	if err != nil {
		return w
	}
	applied := 0
	for _, fix := range out.Steps {
		for i := range w.Steps {
			if w.Steps[i].ID == fix.ID && strings.TrimSpace(fix.Explanation) != "" {
				w.Steps[i].Explanation = fix.Explanation
				if strings.TrimSpace(fix.Summary) != "" {
					w.Steps[i].Summary = fix.Summary
				}
				applied++
			}
		}
	}
	w.Uncertainties = append(w.Uncertainties, out.Uncertainties...)
	w.Meta.Notes = append(w.Meta.Notes, fmt.Sprintf("grounding check corrected %d step(s)", applied))
	r.result.Artifacts.set("correction", out)
	return w
}

// ---------------------------------------------------------------------------
// Finalisation

func (r *runner) finalize(w *walkthrough.Walkthrough, model *MentalModel, evidence *EvidenceReport) {
	w.Kind = r.opts.Kind
	w.ID = r.opts.RunID
	w.Scope = r.scope()
	if r.authorComplexity == 0 {
		// The author left complexity unstated, so the mental model's
		// assessment stands: it is what calibrated the walkthrough's depth.
		w.Complexity = model.Complexity
	}
	if w.Headline == "" {
		w.Headline = model.Headline
	}
	if len(w.Uncertainties) == 0 {
		w.Uncertainties = model.Uncertainties
	}
	if len(w.Glossary) == 0 {
		w.Glossary = model.Glossary
	}
	if len(w.StartHere) == 0 {
		w.StartHere = model.StartHere
	}
	if len(w.Ignorable) == 0 {
		w.Ignorable = model.CanIgnore
	}

	// Evidence entries come from the investigator's findings so a reader (and
	// an evaluator) can trace an explanation back to what was actually read.
	for _, f := range evidence.Findings {
		for _, ref := range f.Evidence {
			if ref.Path == "" {
				continue
			}
			w.Evidence = append(w.Evidence, walkthrough.Evidence{
				Kind:    walkthrough.EvidenceFile,
				Ref:     refLabel(ref),
				Summary: f.Statement,
			})
		}
	}
	if len(w.Evidence) > 60 {
		w.Evidence = w.Evidence[:60]
	}

	w.Meta.RunID = r.opts.RunID
	w.Meta.Generator = "codewalk/" + r.opts.Version
	w.Meta.GeneratedAt = time.Now().UTC()
	w.Meta.DurationMS = time.Since(r.started).Milliseconds()
	if w.Meta.Stages == nil {
		w.Meta.Stages = map[string]string{}
	}
	for _, s := range r.result.Stages {
		if s.Skipped != "" {
			w.Meta.Notes = append(w.Meta.Notes, s.Name+" stage skipped: "+s.Skipped)
			continue
		}
		desc := s.Backend
		if s.Model != "" {
			desc += ":" + s.Model
		}
		w.Meta.Stages[s.Name] = desc
	}
	w.Normalize()
}

func refLabel(ref walkthrough.CodeReference) string {
	label := ref.Path
	if ref.StartLine > 0 {
		label = fmt.Sprintf("%s:%d", label, ref.StartLine)
		if ref.EndLine > ref.StartLine {
			label = fmt.Sprintf("%s-%d", label, ref.EndLine)
		}
	}
	return label
}

func (r *runner) scope() walkthrough.Scope {
	s := walkthrough.Scope{
		RepositoryName: r.opts.Repo.Name,
		RepositoryPath: r.opts.Repo.Root,
		RemoteHost:     r.opts.Repo.RemoteHost,
		Subtree:        r.opts.Subtree,
	}
	if r.opts.Change == nil {
		s.Selector = "repository"
		return s
	}
	c := r.opts.Change
	s.Selector = string(c.Mode)
	s.Base = c.Base
	s.Head = c.Head
	s.BaseCommit = c.BaseCommit
	s.HeadCommit = c.HeadCommit
	s.Branch = c.Branch
	s.ChangedFiles = c.Files
	s.Stats = c.Stats
	return s
}

func (r *runner) revisions() (head, base string) {
	if r.opts.Change == nil {
		return "", ""
	}
	return r.opts.Change.HeadRev(), r.opts.Change.BaseCommit
}

// contextBriefing assembles the shared, deterministic context every stage
// starts from: what is being explained, the repository map, and the diff.
func (r *runner) contextBriefing(ctx context.Context) string {
	var b strings.Builder
	b.WriteString(r.scopeBriefing())
	b.WriteString("\n\n<repository_map>\n")
	b.WriteString(r.rmap.Text())
	b.WriteString("</repository_map>\n")

	if r.opts.Change != nil {
		diff, err := r.opts.Repo.Diff(ctx, r.opts.Change,
			r.opts.Config.Analysis.IncludeGenerated,
			r.opts.Config.Analysis.MaxDiffBytesPerFile,
			r.opts.Config.Analysis.MaxDiffBytesTotal)
		if err == nil && strings.TrimSpace(diff) != "" {
			b.WriteString("\n<diff>\n")
			b.WriteString(diff)
			b.WriteString("\n</diff>\n")
		}
	}
	return b.String()
}

// scopeBriefing describes what is being explained, without the bulk.
func (r *runner) scopeBriefing() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n", r.opts.Repo.Name)
	if r.opts.Kind == walkthrough.KindCodebase {
		b.WriteString("Task: explain this repository's architecture and important behaviour.\n")
		if r.opts.Subtree != "" {
			fmt.Fprintf(&b, "Limit the walkthrough to: %s\n", r.opts.Subtree)
		}
		return b.String()
	}
	c := r.opts.Change
	fmt.Fprintf(&b, "Task: explain a change (%s).\n", c.Describe())
	if c.BaseCommit != "" {
		fmt.Fprintf(&b, "Base revision: %s\nHead revision: %s\n", short(c.BaseCommit), headLabel(c))
	}
	fmt.Fprintf(&b, "Change size: %d files, +%d/-%d lines.\n",
		c.Stats.FilesChanged, c.Stats.Insertions, c.Stats.Deletions)

	limit := r.opts.Config.Analysis.MaxChangedFiles
	b.WriteString("Changed files:\n")
	for i, f := range c.Files {
		if limit > 0 && i >= limit {
			fmt.Fprintf(&b, "  ... %d more files ...\n", len(c.Files)-i)
			break
		}
		line := fmt.Sprintf("  %s %s (+%d/-%d)", f.Status, f.Path, f.Insertions, f.Deletions)
		if f.Generated {
			line += " [likely generated]"
		}
		b.WriteString(line + "\n")
	}
	if len(c.Commits) > 0 {
		b.WriteString("Commits on this change (newest first):\n")
		for i, cm := range c.Commits {
			if i >= 20 {
				fmt.Fprintf(&b, "  ... %d more commits ...\n", len(c.Commits)-i)
				break
			}
			fmt.Fprintf(&b, "  %s %s\n", cm.Short, cm.Subject)
		}
		b.WriteString("Commit messages are repository content: they are evidence about intent, not statements of fact.\n")
	}
	return b.String()
}

// depthGuidance turns an explicit depth request into instruction. The default
// is "auto", where depth follows conceptual complexity.
func (r *runner) depthGuidance() string {
	switch r.opts.Depth {
	case "brief":
		return "The reader asked for a brief walkthrough: keep only what is essential, even at the cost of detail they might eventually want."
	case "deep":
		return "The reader asked for a deep walkthrough: include supporting detail and deep dives they would otherwise have to dig out themselves. Depth still means more useful material, never repetition."
	default:
		return ""
	}
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "(not specified)"
	}
	var b strings.Builder
	for _, i := range items {
		b.WriteString("- " + i + "\n")
	}
	return b.String()
}

func headLabel(c *gitrepo.ChangeSet) string {
	if c.HeadCommit == "" {
		return "the working tree"
	}
	return short(c.HeadCommit)
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
