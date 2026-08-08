package literature

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

const (
	defaultZoteroAPI = "http://127.0.0.1:23119/api/users/0"
	maxResponseBytes = 4 << 20
)

type Profile struct {
	Endpoint       string `json:"endpoint"`
	TokenFile      string `json:"token_file"`
	CollectionKey  string `json:"collection_key,omitempty"`
	CorpusRevision string `json:"corpus_revision,omitempty"`
}

type profileConfig struct {
	Profiles map[string]Profile `json:"profiles"`
}

type Collection struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	ParentKey string `json:"parent_key,omitempty"`
	Path      string `json:"path"`
	Depth     int    `json:"depth"`
	URI       string `json:"uri"`
}

type ProfileStatus struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	CollectionKey  string `json:"zotero_collection_key,omitempty"`
	CorpusRevision string `json:"corpus_revision,omitempty"`
	Documents      int    `json:"documents,omitempty"`
	Chunks         int    `json:"chunks,omitempty"`
	Freshness      string `json:"freshness,omitempty"`
	Error          string `json:"error,omitempty"`
}

type Catalog struct {
	Collections    []Collection    `json:"collections"`
	Profiles       []ProfileStatus `json:"profiles"`
	LibraryVersion int64           `json:"library_version,omitempty"`
}

type QueryRequest struct {
	Query            string `json:"query"`
	EvidenceK        int    `json:"evidence_k,omitempty"`
	AnswerMaxSources int    `json:"answer_max_sources,omitempty"`
}

type Client struct {
	ProfilesPath string
	ZoteroAPI    string
	HTTPClient   *http.Client
}

func NewClient() *Client {
	return &Client{ZoteroAPI: defaultZoteroAPI, HTTPClient: http.DefaultClient}
}

func (c *Client) Catalog(ctx context.Context) (*Catalog, error) {
	collections, libraryVersion, collectionsErr := c.collections(ctx)
	profiles, profilesErr := c.profileStatuses(ctx)
	if collectionsErr != nil && profilesErr != nil {
		return nil, fmt.Errorf("zotero collections: %v; literature profiles: %v", collectionsErr, profilesErr)
	}
	result := &Catalog{Collections: collections, Profiles: profiles, LibraryVersion: libraryVersion}
	if collectionsErr != nil {
		result.Collections = []Collection{}
		result.Profiles = append(result.Profiles, ProfileStatus{Name: "zotero-live", Status: "unavailable", Error: collectionsErr.Error()})
	}
	if profilesErr != nil {
		result.Profiles = append(result.Profiles, ProfileStatus{Name: "profiles", Status: "unavailable", Error: profilesErr.Error()})
	}
	return result, nil
}

func (c *Client) Status(ctx context.Context, project *store.ProjectDefinition, timeout time.Duration) (map[string]interface{}, error) {
	profile, configPath, blocked, err := c.resolve(project)
	if err != nil {
		return nil, err
	}
	if blocked != nil {
		return blocked, nil
	}
	var health map[string]interface{}
	if err := c.request(ctx, profile, configPath, http.MethodGet, profileHealthPath(profile), nil, timeout, &health); err != nil {
		return nil, err
	}
	result := projectResult(project, health)
	if remote := stringValue(health["zotero_collection_key"]); remote != "" && remote != project.ZoteroCollectionKey {
		result["status"] = "blocked"
		result["code"] = "LITERATURE_COLLECTION_MISMATCH"
		result["detail"] = fmt.Sprintf("project expects collection %s but profile currently serves %s", project.ZoteroCollectionKey, remote)
	}
	return result, nil
}

func (c *Client) Query(ctx context.Context, project *store.ProjectDefinition, input QueryRequest, timeout time.Duration) (map[string]interface{}, error) {
	input.Query = strings.TrimSpace(input.Query)
	if len([]rune(input.Query)) < 3 {
		return nil, fmt.Errorf("QUERY_TOO_SHORT: query must contain at least 3 characters")
	}
	profile, configPath, blocked, err := c.resolve(project)
	if err != nil {
		return nil, err
	}
	if blocked != nil {
		return nil, fmt.Errorf("%s: %s", stringValue(blocked["code"]), stringValue(blocked["detail"]))
	}
	if input.EvidenceK <= 0 {
		input.EvidenceK = 10
	}
	if input.AnswerMaxSources <= 0 {
		input.AnswerMaxSources = 6
	}
	body := map[string]interface{}{
		"query": input.Query, "backend": "paperqa2",
		"evidence_k": input.EvidenceK, "answer_max_sources": input.AnswerMaxSources,
	}
	if profile.CorpusRevision != "" {
		body["corpus_revision"] = profile.CorpusRevision
	}
	var response map[string]interface{}
	if err := c.request(ctx, profile, configPath, http.MethodPost, "/query", body, timeout, &response); err != nil {
		return nil, err
	}
	if remote := stringValue(response["zotero_collection_key"]); remote != project.ZoteroCollectionKey {
		return nil, fmt.Errorf("LITERATURE_COLLECTION_MISMATCH: project expects %s but response used %s", project.ZoteroCollectionKey, remote)
	}
	response["project_id"] = project.ID
	response["project_zotero_collection_key"] = project.ZoteroCollectionKey
	response["literature_service_profile"] = project.LiteratureServiceProfile
	response["evidence_domain"] = "literature"
	response["claim_scope"] = "background_only"
	return response, nil
}

func (c *Client) resolve(project *store.ProjectDefinition) (Profile, string, map[string]interface{}, error) {
	if project == nil {
		return Profile{}, "", nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(project.ZoteroCollectionKey) == "" || strings.TrimSpace(project.LiteratureServiceProfile) == "" {
		return Profile{}, "", map[string]interface{}{
			"status": "blocked", "code": "LITERATURE_NOT_BOUND", "project_id": project.ID,
			"detail": "choose one Zotero collection and a ready literature profile before querying",
		}, nil
	}
	configPath, config, err := c.loadProfiles()
	if err != nil {
		return Profile{}, configPath, map[string]interface{}{
			"status": "blocked", "code": "LITERATURE_PROFILE_CONFIG_UNAVAILABLE", "project_id": project.ID,
			"service_profile": project.LiteratureServiceProfile, "detail": err.Error(),
		}, nil
	}
	profile, ok := config.Profiles[project.LiteratureServiceProfile]
	if !ok || strings.TrimSpace(profile.Endpoint) == "" || strings.TrimSpace(profile.TokenFile) == "" {
		return profile, configPath, map[string]interface{}{
			"status": "blocked", "code": "LITERATURE_PROFILE_NOT_CONFIGURED", "project_id": project.ID,
			"service_profile": project.LiteratureServiceProfile, "detail": "profile requires endpoint and token_file",
		}, nil
	}
	if profile.CollectionKey != "" && profile.CollectionKey != project.ZoteroCollectionKey {
		return profile, configPath, map[string]interface{}{
			"status": "blocked", "code": "LITERATURE_PROFILE_COLLECTION_MISMATCH", "project_id": project.ID,
			"service_profile": project.LiteratureServiceProfile,
			"detail":          fmt.Sprintf("profile serves collection %s, not %s", profile.CollectionKey, project.ZoteroCollectionKey),
		}, nil
	}
	return profile, configPath, nil, nil
}

func (c *Client) profileStatuses(ctx context.Context) ([]ProfileStatus, error) {
	configPath, config, err := c.loadProfiles()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ProfileStatus, 0, len(names))
	for _, name := range names {
		profile := config.Profiles[name]
		var health map[string]interface{}
		status := ProfileStatus{Name: name, Status: "unavailable"}
		if err := c.request(ctx, profile, configPath, http.MethodGet, profileHealthPath(profile), nil, 4*time.Second, &health); err != nil {
			status.Error = err.Error()
			result = append(result, status)
			continue
		}
		status.Status = stringValue(health["status"])
		status.CollectionKey = stringValue(health["zotero_collection_key"])
		status.CorpusRevision = stringValue(health["corpus_revision"])
		status.Freshness = stringValue(health["freshness"])
		if backends, ok := health["backends"].(map[string]interface{}); ok {
			if paperqa, ok := backends["paperqa2"].(map[string]interface{}); ok {
				status.Documents = intValue(paperqa["documents"])
				status.Chunks = intValue(paperqa["chunks"])
			}
		}
		result = append(result, status)
	}
	return result, nil
}

func profileHealthPath(profile Profile) string {
	if strings.TrimSpace(profile.CorpusRevision) == "" {
		return "/health"
	}
	return "/health?corpus_revision=" + url.QueryEscape(strings.TrimSpace(profile.CorpusRevision))
}

func (c *Client) collections(ctx context.Context) ([]Collection, int64, error) {
	base := strings.TrimRight(strings.TrimSpace(c.ZoteroAPI), "/")
	if base == "" {
		base = defaultZoteroAPI
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	type rawCollection struct {
		Key  string `json:"key"`
		Data struct {
			Key              string      `json:"key"`
			Name             string      `json:"name"`
			ParentCollection interface{} `json:"parentCollection"`
		} `json:"data"`
	}
	raw := make([]rawCollection, 0)
	var libraryVersion int64
	for offset := 0; offset < 1000; offset += 100 {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/collections?limit=100&start=%d", base, offset), nil)
		if err != nil {
			return nil, 0, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, 0, fmt.Errorf("zotero local API: %w", err)
		}
		var page []rawCollection
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&page)
		response.Body.Close()
		if response.StatusCode >= 400 {
			return nil, 0, fmt.Errorf("zotero local API HTTP %d", response.StatusCode)
		}
		if decodeErr != nil {
			return nil, 0, fmt.Errorf("decode Zotero collections: %w", decodeErr)
		}
		if version, _ := strconv.ParseInt(response.Header.Get("Last-Modified-Version"), 10, 64); version > libraryVersion {
			libraryVersion = version
		}
		raw = append(raw, page...)
		if len(page) < 100 {
			break
		}
	}
	byKey := make(map[string]Collection, len(raw))
	for _, item := range raw {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			key = strings.TrimSpace(item.Data.Key)
		}
		parent := ""
		if value, ok := item.Data.ParentCollection.(string); ok {
			parent = strings.TrimSpace(value)
		}
		byKey[key] = Collection{Key: key, Name: strings.TrimSpace(item.Data.Name), ParentKey: parent, URI: "zotero://select/library/collections/" + key}
	}
	var resolvePath func(string, map[string]bool) (string, int)
	resolvePath = func(key string, visiting map[string]bool) (string, int) {
		item, ok := byKey[key]
		if !ok || item.ParentKey == "" || visiting[key] {
			return item.Name, 0
		}
		visiting[key] = true
		parentPath, parentDepth := resolvePath(item.ParentKey, visiting)
		delete(visiting, key)
		if parentPath == "" {
			return item.Name, 0
		}
		return parentPath + " / " + item.Name, parentDepth + 1
	}
	collections := make([]Collection, 0, len(byKey))
	for key, item := range byKey {
		item.Path, item.Depth = resolvePath(key, map[string]bool{})
		collections = append(collections, item)
	}
	sort.Slice(collections, func(i, j int) bool {
		return strings.ToLower(collections[i].Path) < strings.ToLower(collections[j].Path)
	})
	return collections, libraryVersion, nil
}

func (c *Client) loadProfiles() (string, profileConfig, error) {
	path := strings.TrimSpace(c.ProfilesPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("AEXP_LITERATURE_PROFILES"))
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", profileConfig{}, err
		}
		path = filepath.Join(home, ".aexp", "literature-profiles.json")
	}
	path = expandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return path, profileConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var config profileConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return path, config, fmt.Errorf("decode %s: %w", path, err)
	}
	return path, config, nil
}

func (c *Client) request(ctx context.Context, profile Profile, configPath, method, requestPath string, body interface{}, timeout time.Duration, output interface{}) error {
	tokenPath := expandPath(profile.TokenFile)
	if !filepath.IsAbs(tokenPath) {
		tokenPath = filepath.Join(filepath.Dir(configPath), tokenPath)
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("read literature service token: %w", err)
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(profile.Endpoint, "/")+requestPath, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenBytes)))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("literature service request: %w", err)
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(output); err != nil {
		return fmt.Errorf("decode literature service response: %w", err)
	}
	if response.StatusCode >= 400 {
		if payload, ok := output.(*map[string]interface{}); ok {
			return fmt.Errorf("literature service HTTP %d: %v", response.StatusCode, (*payload)["code"])
		}
		return fmt.Errorf("literature service HTTP %d", response.StatusCode)
	}
	return nil
}

func projectResult(project *store.ProjectDefinition, health map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"status": health["status"], "project_id": project.ID,
		"zotero_collection_key": project.ZoteroCollectionKey,
		"service_profile":       project.LiteratureServiceProfile,
		"evidence_domain":       "literature", "claim_scope": "background_only",
		"service": health,
	}
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		result, _ := strconv.Atoi(stringValue(value))
		return result
	}
}
