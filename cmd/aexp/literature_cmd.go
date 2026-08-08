package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ziwu/aexp/internal/literature"
)

func literatureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "literature",
		Short: "Query the read-only literature service bound to a Project",
		Long:  "Literature discovery is read-only background evidence. It cannot satisfy Run, DatasetVersion, Snapshot, Freeze, Release, or accepted Evidence Map provenance.",
	}
	cmd.AddCommand(literatureCatalogCmd(), literatureBindCmd(), literatureStatusCmd(), literatureQueryCmd())
	return cmd
}

func literatureCatalogCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List local Zotero collections and configured frozen-corpus profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			catalog, err := literature.NewClient().Catalog(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(catalog)
			}
			for _, collection := range catalog.Collections {
				fmt.Printf("%s\t%s\n", collection.Key, collection.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func literatureBindCmd() *cobra.Command {
	var collectionKey, serviceProfile string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "bind PROJECT_ID",
		Short: "Bind a Project to one Zotero collection and literature service profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			collectionKey = strings.TrimSpace(collectionKey)
			serviceProfile = strings.TrimSpace(serviceProfile)
			if collectionKey == "" || serviceProfile == "" {
				return fmt.Errorf("--zotero-collection and --service-profile are required")
			}
			catalog, err := literature.NewClient().Catalog(cmd.Context())
			if err != nil {
				return err
			}
			profileReady := false
			for _, profile := range catalog.Profiles {
				if profile.Name != serviceProfile {
					continue
				}
				if profile.Status != "ready" {
					return fmt.Errorf("LITERATURE_PROFILE_NOT_READY: profile %s is %s", serviceProfile, profile.Status)
				}
				if profile.CollectionKey != collectionKey {
					return fmt.Errorf("LITERATURE_COLLECTION_MISMATCH: profile %s serves %s, not %s", serviceProfile, profile.CollectionKey, collectionKey)
				}
				profileReady = true
			}
			if !profileReady {
				return fmt.Errorf("LITERATURE_PROFILE_NOT_CONFIGURED: profile %s is not available", serviceProfile)
			}
			db := openDB()
			defer db.Close()
			project, err := db.GetProjectDefinition(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if project == nil {
				return fmt.Errorf("project %q not found", args[0])
			}
			project.ZoteroCollectionKey = collectionKey
			project.LiteratureServiceProfile = serviceProfile
			if err := db.SaveProjectDefinition(cmd.Context(), project); err != nil {
				return err
			}
			result := map[string]interface{}{
				"status": "bound", "project_id": project.ID,
				"zotero_collection_key": collectionKey, "service_profile": serviceProfile,
				"evidence_domain": "literature", "claim_scope": "background_only",
			}
			if asJSON {
				return printJSON(result)
			}
			fmt.Printf("Bound %s to Zotero collection %s via %s\n", project.ID, collectionKey, serviceProfile)
			return nil
		},
	}
	cmd.Flags().StringVar(&collectionKey, "zotero-collection", "", "Zotero collection key")
	cmd.Flags().StringVar(&serviceProfile, "service-profile", "", "Literature service profile name")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func literatureStatusCmd() *cobra.Command {
	var timeout time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status PROJECT_ID",
		Short: "Check a Project literature binding and active corpus",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			project, err := db.GetProjectDefinition(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if project == nil {
				return fmt.Errorf("project %q not found", args[0])
			}
			result, err := literature.NewClient().Status(cmd.Context(), project, timeout)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(result)
			}
			if result["status"] == "blocked" {
				return fmt.Errorf("%v: %v", result["code"], result["detail"])
			}
			fmt.Printf("%s: %v\n", project.ID, result["status"])
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 20*time.Second, "Request timeout")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func literatureQueryCmd() *cobra.Command {
	var query string
	var evidenceK, answerMaxSources int
	var timeout time.Duration
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "query PROJECT_ID",
		Short: "Query the Project's bound PaperQA2 corpus",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db := openDB()
			defer db.Close()
			project, err := db.GetProjectDefinition(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if project == nil {
				return fmt.Errorf("project %q not found", args[0])
			}
			response, err := literature.NewClient().Query(cmd.Context(), project, literature.QueryRequest{
				Query: query, EvidenceK: evidenceK, AnswerMaxSources: answerMaxSources,
			}, timeout)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(response)
			}
			fmt.Println(response["answer"])
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Literature question")
	cmd.Flags().IntVar(&evidenceK, "evidence-k", 10, "Candidate evidence count")
	cmd.Flags().IntVar(&answerMaxSources, "answer-max-sources", 6, "Maximum cited sources")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Request timeout")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("query")
	return cmd
}
