// Package server exposes codewalk over HTTP and serves the embedded web UI.
//
// The API is local-first: it binds to loopback and is unauthenticated, which is
// appropriate for a developer tool running on a developer's machine and
// inappropriate anywhere else. Binding to a non-loopback address requires an
// explicit opt-in, and cross-origin requests from a browser are rejected, so a
// web page cannot drive a reader's local server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rclod/codewalk/internal/config"
	"github.com/rclod/codewalk/internal/gitrepo"
	"github.com/rclod/codewalk/internal/run"
	"github.com/rclod/codewalk/internal/service"
	"github.com/rclod/codewalk/internal/walkthrough"
	"github.com/rclod/codewalk/web"
)

// Options configures the HTTP server.
type Options struct {
	Service *service.Service
	Config  *config.Config
	Version string
	// DefaultRepository is the repository used when a request does not name one.
	DefaultRepository string
	// AllowRemote permits binding to a non-loopback address. It exists so that
	// exposing an unauthenticated service is always a deliberate act.
	AllowRemote bool
	Logger      *slog.Logger
}

// Server is the codewalk HTTP service.
type Server struct {
	opts Options
	jobs *jobManager
	mux  *http.ServeMux
	log  *slog.Logger
}

// New creates a server.
func New(opts Options) (*Server, error) {
	if opts.Service == nil {
		return nil, errors.New("server: no service")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{opts: opts, jobs: newJobManager(), mux: http.NewServeMux(), log: opts.Logger}
	s.routes()
	return s, nil
}

// Handler returns the HTTP handler, with local-only protection applied.
func (s *Server) Handler() http.Handler {
	return s.protect(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/repository", s.handleRepository)

	s.mux.HandleFunc("POST /api/v1/walkthroughs", s.handleCreateWalkthrough)
	s.mux.HandleFunc("GET /api/v1/walkthroughs", s.handleListWalkthroughs)
	s.mux.HandleFunc("GET /api/v1/walkthroughs/{id}", s.handleGetWalkthrough)
	s.mux.HandleFunc("GET /api/v1/walkthroughs/{id}/artifacts", s.handleListArtifacts)
	s.mux.HandleFunc("GET /api/v1/walkthroughs/{id}/artifacts/{name}", s.handleGetArtifact)
	s.mux.HandleFunc("GET /api/v1/walkthroughs/{id}/conversation", s.handleConversation)
	s.mux.HandleFunc("POST /api/v1/walkthroughs/{id}/questions", s.handleQuestion)
	s.mux.HandleFunc("POST /api/v1/walkthroughs/{id}/feedback", s.handleFeedback)
	s.mux.HandleFunc("GET /api/v1/walkthroughs/{id}/source", s.handleSource)

	s.mux.HandleFunc("GET /api/v1/jobs/{id}", s.handleGetJob)
	s.mux.HandleFunc("GET /api/v1/jobs/{id}/events", s.handleJobEvents)

	s.mux.Handle("GET /", s.staticHandler())
}

// protect applies the local-service safety rules: reject cross-origin browser
// requests (which defends against DNS rebinding), and refuse to serve at all if
// the server was bound remotely without an explicit opt-in.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	origin = strings.TrimSuffix(origin, "/")
	for _, prefix := range []string{"http://localhost", "http://127.0.0.1", "http://[::1]"} {
		if strings.HasPrefix(origin, prefix) {
			return true
		}
	}
	return false
}

// Listen starts the server and blocks until the context is cancelled.
func (s *Server) Listen(ctx context.Context, host string, port int) (string, error) {
	if !s.opts.AllowRemote && !isLoopback(host) {
		return "", fmt.Errorf("refusing to bind %s: the API is unauthenticated, so binding outside loopback needs --allow-remote", host)
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	url := "http://" + ln.Addr().String()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server stopped", "error", err)
		}
	}()
	return url, nil
}

func isLoopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---------------------------------------------------------------------------
// Handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        s.opts.Version,
		"schema_version": walkthrough.SchemaVersion,
	})
}

func (s *Server) handleRepository(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = s.opts.DefaultRepository
	}
	repo, err := gitrepo.Discover(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	dirty, _ := repo.HasUncommittedChanges(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":           repo.Name,
		"branch":         repo.CurrentBranch(ctx),
		"default_branch": repo.DefaultBranch(ctx),
		"has_changes":    dirty,
	})
}

// createWalkthroughRequest is the API request body. It mirrors the CLI so both
// surfaces expose the same capabilities.
type createWalkthroughRequest struct {
	// Type is "change" (aliases: "pr", "diff") or "codebase".
	Type       string `json:"type"`
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Head       string `json:"head"`
	// Selector is auto, working-tree, staged, branch, range or commit.
	Selector string `json:"selector"`
	// Range is a revision expression such as "main..feature".
	Range   string `json:"range"`
	Depth   string `json:"depth"`
	Focus   string `json:"focus"`
	Subtree string `json:"subtree"`
	Backend string `json:"backend"`
	Model   string `json:"model"`
	// Wait makes the request block until generation finishes. The default is
	// asynchronous, which is what the web UI uses.
	Wait bool `json:"wait"`
}

func (s *Server) handleCreateWalkthrough(w http.ResponseWriter, r *http.Request) {
	var req createWalkthroughRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	genReq, err := s.buildGenerateRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if req.Wait {
		result, err := s.opts.Service.Generate(r.Context(), genReq)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"run":         result.Run,
			"walkthrough": result.Walkthrough,
		})
		return
	}

	// Generation outlives the request, so it runs on a background context that
	// the job can cancel.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	jobID := run.NewID(time.Now())
	j := s.jobs.create(jobID, cancel)
	genReq.Observer = j

	go func() {
		defer cancel()
		result, err := s.opts.Service.Generate(ctx, genReq)
		if err != nil {
			s.log.Warn("walkthrough generation failed", "job", jobID, "error", err)
			j.finish("failed", "", err.Error())
			return
		}
		j.finish("complete", result.Run.ID, "")
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": jobID,
		"status": "running",
		"links": map[string]string{
			"job":    "/api/v1/jobs/" + jobID,
			"events": "/api/v1/jobs/" + jobID + "/events",
		},
	})
}

func (s *Server) buildGenerateRequest(req createWalkthroughRequest) (service.GenerateRequest, error) {
	kind := walkthrough.KindChange
	switch strings.ToLower(req.Type) {
	case "", "change", "pr", "diff":
		kind = walkthrough.KindChange
	case "codebase", "repo", "repository":
		kind = walkthrough.KindCodebase
	default:
		return service.GenerateRequest{}, fmt.Errorf("unknown walkthrough type %q", req.Type)
	}

	repoPath := req.Repository
	if repoPath == "" {
		repoPath = s.opts.DefaultRepository
	}
	if repoPath == "" {
		return service.GenerateRequest{}, errors.New("no repository given")
	}

	sel := gitrepo.Selection{Mode: gitrepo.ModeAuto, Base: req.Base, Head: req.Head, Spec: req.Range}
	switch gitrepo.SelectorMode(req.Selector) {
	case gitrepo.ModeStaged:
		sel.Mode = gitrepo.ModeStaged
	case gitrepo.ModeWorkingTree:
		sel.Mode = gitrepo.ModeWorkingTree
	case gitrepo.ModeBranch:
		sel.Mode = gitrepo.ModeBranch
	case gitrepo.ModeRange, gitrepo.ModeCommit:
		sel.Mode = gitrepo.SelectorMode(req.Selector)
	}

	return service.GenerateRequest{
		RepositoryPath:  repoPath,
		Kind:            kind,
		Selection:       sel,
		Depth:           req.Depth,
		Focus:           req.Focus,
		Subtree:         req.Subtree,
		BackendOverride: req.Backend,
		ModelOverride:   req.Model,
	}, nil
}

func (s *Server) handleListWalkthroughs(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.opts.Service.Store().List(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if summaries == nil {
		summaries = []run.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"walkthroughs": summaries})
}

func (s *Server) handleGetWalkthrough(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store := s.opts.Service.Store()
	record, err := store.Load(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	wt, err := store.Walkthrough(record.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"run": record})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": record, "walkthrough": wt})
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	id, err := s.opts.Service.Store().Resolve(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": s.opts.Service.Store().ArtifactNames(id)})
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	store := s.opts.Service.Store()
	id, err := store.Resolve(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	data, err := store.Artifact(id, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	record, err := s.opts.Service.Store().Load(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	turns := record.Conversation
	if turns == nil {
		turns = []run.Turn{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": turns})
}

func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		writeError(w, http.StatusBadRequest, errors.New("question is required"))
		return
	}
	res, err := s.opts.Service.Ask(r.Context(), service.AskRequest{
		RunID:    r.PathValue("id"),
		Question: body.Question,
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// feedbackRequest captures the lightweight human signal the product ultimately
// cares about: did this walkthrough give the reader the mental model they
// needed before reading the code?
type feedbackRequest struct {
	// Answer is "yes", "mostly" or "no".
	Answer string `json:"answer"`
	// Sections carries optional per-step signal, keyed by step id, with values
	// such as "helpful", "unclear", "incorrect", "too_detailed", "missing_context".
	Sections map[string]string `json:"sections,omitempty"`
	Comment  string            `json:"comment,omitempty"`
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var body feedbackRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch body.Answer {
	case "yes", "mostly", "no", "":
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("answer must be yes, mostly or no"))
		return
	}
	payload := map[string]any{
		"answer":      body.Answer,
		"sections":    body.Sections,
		"comment":     body.Comment,
		"recorded_at": time.Now().UTC(),
	}
	if err := s.opts.Service.Store().SaveFeedback(r.PathValue("id"), payload); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "recorded"})
}

// handleSource returns a slice of a source file so the UI can show code beside
// an explanation without the reader leaving the walkthrough.
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	store := s.opts.Service.Store()
	record, err := store.Load(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	repoPath := record.Repository.Path
	if repoPath == "" {
		repoPath = s.opts.DefaultRepository
	}
	repo, err := gitrepo.Discover(repoPath)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	rev := r.URL.Query().Get("rev")
	if rev == "" && r.URL.Query().Get("side") == "before" {
		rev = record.Scope.BaseCommit
	}
	// Reuse the tool sandbox rules rather than reimplementing path safety here.
	content, err := readRepoFile(r.Context(), repo, rev, path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    path,
		"rev":     rev,
		"content": content,
	})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	snapshot := j.snapshot()
	writeJSON(w, http.StatusOK, snapshot)
}

// handleJobEvents streams progress as Server-Sent Events.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	history, ch := j.subscribe()
	defer j.unsubscribe(ch)
	for _, e := range history {
		writeSSE(w, "progress", e)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				writeSSE(w, "done", j.snapshot())
				flusher.Flush()
				return
			}
			writeSSE(w, "progress", e)
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func (s *Server) staticHandler() http.Handler {
	fileSystem := web.FS()
	fileServer := http.FileServer(http.FS(fileSystem))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(fileSystem, clean); err != nil {
			// Unknown paths fall back to the app shell so client-side routes work.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
