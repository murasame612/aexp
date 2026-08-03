package store

import (
	"context"
	"fmt"
	"strings"
)

const ProjectResearchContextVersion = "project-research-context-v1"

type ProjectResearchContextOptions struct {
	MapLimit     int
	ThreadLimit  int
	JournalLimit int
	RunLimit     int
}

type ProjectResearchProjectSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ProjectResearchThreadSummary struct {
	Title     string   `json:"title"`
	Phase     string   `json:"phase"`
	NodeCount int      `json:"node_count"`
	Outcomes  []string `json:"outcomes,omitempty"`
}

type ProjectResearchMapSummary struct {
	ID                  string                         `json:"id"`
	Title               string                         `json:"title"`
	Purpose             string                         `json:"purpose,omitempty"`
	Revision            int64                          `json:"revision"`
	RoutingHints        EvidenceGraphRoutingHints      `json:"routing_hints"`
	ThreadCount         int                            `json:"thread_count"`
	UnassignedCount     int                            `json:"unassigned_count"`
	CompatibilityStatus string                         `json:"compatibility_status"`
	ReadabilityStatus   string                         `json:"readability_status"`
	SplitRecommended    bool                           `json:"split_recommended"`
	Threads             []ProjectResearchThreadSummary `json:"threads,omitempty"`
}

type ProjectResearchJournalSummary struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Actor            string   `json:"actor"`
	NextAction       string   `json:"next_action,omitempty"`
	NextActionStatus string   `json:"next_action_status"`
	RunIDs           []string `json:"run_ids,omitempty"`
	CreatedAt        string   `json:"created_at"`
}

type ProjectResearchProposalSummary struct {
	ID           string `json:"id"`
	TargetMapID  string `json:"target_map_id,omitempty"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	BaseRevision int64  `json:"base_graph_revision"`
	UpdatedAt    string `json:"updated_at"`
}

type ProjectResearchRunContext struct {
	Total  int                         `json:"total"`
	Active int                         `json:"active"`
	Recent []ProjectResearchRunSummary `json:"recent"`
}

type ProjectResearchRunSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ResourceID    string `json:"resource_id"`
	Status        string `json:"status"`
	Freshness     string `json:"freshness"`
	Kind          string `json:"kind"`
	EvidenceGrade string `json:"evidence_grade"`
	CreatedAt     string `json:"created_at"`
}

type ProjectResearchContextWarning struct {
	Code    string `json:"code"`
	MapID   string `json:"map_id,omitempty"`
	Message string `json:"message"`
}

type ProjectResearchNextRead struct {
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
	Reason string                 `json:"reason"`
}

type ProjectResearchContext struct {
	ContractVersion  string                           `json:"contract_version"`
	Project          ProjectResearchProjectSummary    `json:"project"`
	PrimaryMap       *ProjectResearchMapSummary       `json:"primary_map,omitempty"`
	Topics           []ProjectResearchMapSummary      `json:"topics"`
	Journal          []ProjectResearchJournalSummary  `json:"recent_journal"`
	Runs             ProjectResearchRunContext        `json:"runs"`
	PendingProposals []ProjectResearchProposalSummary `json:"pending_proposals"`
	Warnings         []ProjectResearchContextWarning  `json:"warnings"`
	NextReads        []ProjectResearchNextRead        `json:"next_reads"`
}

func normalizeProjectResearchContextOptions(options ProjectResearchContextOptions) ProjectResearchContextOptions {
	clamp := func(value, fallback, maximum int) int {
		if value <= 0 {
			return fallback
		}
		if value > maximum {
			return maximum
		}
		return value
	}
	options.MapLimit = clamp(options.MapLimit, 6, 20)
	options.ThreadLimit = clamp(options.ThreadLimit, 1, 8)
	options.JournalLimit = clamp(options.JournalLimit, 5, 20)
	options.RunLimit = clamp(options.RunLimit, 4, 30)
	return options
}

func compactResearchText(value string, maximum int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) <= maximum {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maximum-1])) + "…"
}

func researchStageTitles(cards []EvidenceResearchCard, limit int) []string {
	values := make([]string, 0, limit)
	for _, card := range cards {
		if title := compactResearchText(card.Node.Title, 96); title != "" {
			values = append(values, title)
		}
		if len(values) >= limit {
			break
		}
	}
	return values
}

func projectResearchMapSummary(chain EvidenceChain, projection EvidenceResearchProjection, threadLimit int) ProjectResearchMapSummary {
	routingHints := EvidenceGraphRoutingHints{
		Recipes:  append([]string(nil), chain.RoutingHints.Recipes...),
		Keywords: append([]string(nil), chain.RoutingHints.Keywords...),
	}
	if len(routingHints.Recipes) > 2 {
		routingHints.Recipes = routingHints.Recipes[:2]
	}
	if len(routingHints.Keywords) > 2 {
		routingHints.Keywords = routingHints.Keywords[:2]
	}
	summary := ProjectResearchMapSummary{
		ID: chain.ID, Title: compactResearchText(chain.Title, 120), Purpose: compactResearchText(chain.Description, 180),
		Revision:     chain.Revision,
		RoutingHints: routingHints, ThreadCount: len(projection.Threads), UnassignedCount: len(projection.Unassigned),
		CompatibilityStatus: projection.StructuralHealth.CompatibilityStatus,
		ReadabilityStatus:   projection.StructuralHealth.ReadabilityStatus,
		SplitRecommended:    projection.Capacity.SplitRecommended,
		Threads:             make([]ProjectResearchThreadSummary, 0, threadLimit),
	}
	for _, thread := range projection.Threads {
		if len(summary.Threads) >= threadLimit {
			break
		}
		phase := ""
		nodeCount := 0
		for _, health := range projection.StructuralHealth.Threads {
			if health.ThreadID == thread.ID {
				phase = health.DerivedPhase
				nodeCount = health.SemanticNodeCount
				break
			}
		}
		outcomes := append(researchStageTitles(thread.Stages[EvidenceResearchStageConclusion], 1), researchStageTitles(thread.Stages[EvidenceResearchStageIssue], 1)...)
		if len(outcomes) > 1 {
			outcomes = outcomes[:1]
		}
		summary.Threads = append(summary.Threads, ProjectResearchThreadSummary{
			Title: compactResearchText(thread.Title, 110), Phase: phase, NodeCount: nodeCount, Outcomes: outcomes,
		})
	}
	return summary
}

func (s *SQLite) GetProjectResearchContext(ctx context.Context, projectID string, options ProjectResearchContextOptions) (*ProjectResearchContext, error) {
	projectID = strings.TrimSpace(projectID)
	project, err := s.GetProjectDefinition(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	options = normalizeProjectResearchContextOptions(options)
	result := &ProjectResearchContext{
		ContractVersion: ProjectResearchContextVersion,
		Project: ProjectResearchProjectSummary{
			ID: project.ID, Name: compactResearchText(project.Name, 100), Description: compactResearchText(project.Description, 140),
		},
		Topics: []ProjectResearchMapSummary{}, Journal: []ProjectResearchJournalSummary{},
		PendingProposals: []ProjectResearchProposalSummary{}, Warnings: []ProjectResearchContextWarning{},
		NextReads: []ProjectResearchNextRead{},
	}

	primaryMaps, err := s.ListEvidenceChains(ctx, EvidenceChainFilter{ProjectID: projectID, Role: "primary", Status: "active", Limit: 1})
	if err != nil {
		return nil, err
	}
	topicMaps, err := s.ListEvidenceChains(ctx, EvidenceChainFilter{ProjectID: projectID, Role: "secondary", Status: "active", Limit: options.MapLimit})
	if err != nil {
		return nil, err
	}
	chains := append(primaryMaps, topicMaps...)
	for _, chain := range chains {
		graph, err := s.GetEvidenceChainGraph(ctx, chain.ID)
		if err != nil {
			return nil, err
		}
		if graph == nil {
			return nil, fmt.Errorf("evidence Map %q has no readable graph", chain.ID)
		}
		projection := BuildEvidenceResearchProjection(chain, *graph)
		summary := projectResearchMapSummary(chain, projection, options.ThreadLimit)
		if chain.Role == "primary" {
			copy := summary
			result.PrimaryMap = &copy
		} else {
			result.Topics = append(result.Topics, summary)
		}
		if summary.UnassignedCount > 0 {
			result.Warnings = append(result.Warnings, ProjectResearchContextWarning{Code: "UNASSIGNED_EVIDENCE", MapID: chain.ID, Message: fmt.Sprintf("%d accepted items still need thread routing", summary.UnassignedCount)})
		}
		if summary.SplitRecommended {
			result.Warnings = append(result.Warnings, ProjectResearchContextWarning{Code: "TOPIC_SPLIT_RECOMMENDED", MapID: chain.ID, Message: "Topic presentation load should be reviewed before adding another independent hypothesis"})
		}
		result.NextReads = append(result.NextReads, ProjectResearchNextRead{Tool: "aexp_get_evidence_thread_map", Args: map[string]interface{}{"map_id": chain.ID}, Reason: "Read only if this Topic matches the task."})
	}
	if len(chains) == 0 {
		result.Warnings = append(result.Warnings, ProjectResearchContextWarning{Code: "NO_ACTIVE_EVIDENCE_MAP", Message: "Project has no active Evidence Map"})
	}

	entries, err := s.ListProjectJournalEntries(ctx, ProjectJournalFilter{ProjectID: projectID, Limit: options.JournalLimit})
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		result.Journal = append(result.Journal, ProjectResearchJournalSummary{
			ID: entry.ID, Title: compactResearchText(entry.Title, 120), Actor: entry.Actor,
			NextAction: compactResearchText(entry.NextAction, 180), NextActionStatus: entry.NextActionStatus,
			RunIDs: append([]string(nil), entry.RunIDs...), CreatedAt: entry.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
		if entry.NextActionStatus == JournalNextActionOpen {
			result.NextReads = append(result.NextReads, ProjectResearchNextRead{Tool: "aexp_get_project_journal_entry", Args: map[string]interface{}{"entry_id": entry.ID}, Reason: "Read before acting on this open next action."})
		}
	}

	total, err := s.CountRuns(ctx, RunFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	active, err := s.CountRuns(ctx, RunFilter{ProjectID: projectID, Active: true})
	if err != nil {
		return nil, err
	}
	recent, err := s.ListRunSummaries(ctx, RunFilter{ProjectID: projectID, Limit: options.RunLimit})
	if err != nil {
		return nil, err
	}
	compactRuns := make([]ProjectResearchRunSummary, 0, len(recent))
	for _, run := range recent {
		compactRuns = append(compactRuns, ProjectResearchRunSummary{
			ID: run.ID, Name: compactResearchText(run.Name, 120), ResourceID: run.ResourceID,
			Status: run.Status, Freshness: run.StatusFreshness,
			Kind: run.Kind, EvidenceGrade: run.EvidenceGrade, CreatedAt: run.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	result.Runs = ProjectResearchRunContext{Total: total, Active: active, Recent: compactRuns}
	for _, run := range recent {
		if run.Status == RunStatusRunning || run.Status == RunStatusStarting || run.Status == RunStatusFailed {
			result.NextReads = append(result.NextReads, ProjectResearchNextRead{Tool: "aexp_get_run_snapshot", Args: map[string]interface{}{"run_id": run.ID}, Reason: "Inspect this active or failed Run."})
			break
		}
	}

	proposals, err := s.ListEvidenceProposals(ctx, EvidenceProposalFilter{ProjectID: projectID, Status: GraphProposalPending, Limit: 8})
	if err != nil {
		return nil, err
	}
	for _, proposal := range proposals {
		result.PendingProposals = append(result.PendingProposals, ProjectResearchProposalSummary{
			ID: proposal.ID, TargetMapID: proposal.TargetChainID, Summary: compactResearchText(proposal.Summary, 140),
			Status: proposal.Status, BaseRevision: proposal.BaseGraphRevision, UpdatedAt: proposal.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	if len(proposals) > 0 {
		result.Warnings = append(result.Warnings, ProjectResearchContextWarning{Code: "PENDING_EVIDENCE_PROPOSALS", Message: fmt.Sprintf("%d reviewable Evidence proposals are pending", len(proposals))})
	}
	if len(result.NextReads) > 12 {
		result.NextReads = result.NextReads[:12]
	}
	return result, nil
}
