package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"actrail/internal/importer"
)

func main() {
	var (
		sourceSQLite = flag.String("source-sqlite", "", "path to Codoxear sqlite snapshot")
		targetSQLite = flag.String("target-sqlite", "", "path to ActRail sqlite target")
		sideDir      = flag.String("side-dir", "", "optional directory containing legacy side JSON files for freshness audit")
		reportJSON   = flag.String("report-json", "", "optional path to write report JSON")
		snapshotAt   = flag.String("snapshot-at", "", "optional RFC3339 snapshot timestamp; defaults to now")
		jsonStdout   = flag.Bool("json", false, "write JSON report to stdout")
	)
	flag.Parse()

	opts := importer.Options{
		SourceSQLitePath: strings.TrimSpace(*sourceSQLite),
		TargetSQLitePath: strings.TrimSpace(*targetSQLite),
		SideDir:          strings.TrimSpace(*sideDir),
	}
	if strings.TrimSpace(*snapshotAt) != "" {
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(*snapshotAt))
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse -snapshot-at: %v\n", err)
			os.Exit(1)
		}
		opts.SnapshotAt = ts
	}
	report, err := importer.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "actrail-importer: %v\n", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(*reportJSON) != "" {
		if err := os.WriteFile(strings.TrimSpace(*reportJSON), append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
	}
	if *jsonStdout {
		_, _ = os.Stdout.Write(append(encoded, '\n'))
		return
	}
	printTextReport(report)
}

func printTextReport(report importer.Report) {
	fmt.Printf("snapshot_at: %s\n", report.SnapshotAt.UTC().Format(time.RFC3339))
	fmt.Printf("source_sqlite: %s\n", report.SourceSQLitePath)
	fmt.Printf("target_sqlite: %s\n", report.TargetSQLitePath)
	fmt.Println("source_counts:")
	fmt.Printf("  sessions: %d\n", report.SourceCounts.Sessions)
	fmt.Printf("  session_ui_state: %d\n", report.SourceCounts.SessionUIState)
	fmt.Printf("  hidden_session_keys: %d\n", report.SourceCounts.HiddenSessionKeys)
	fmt.Printf("  session_files: %d\n", report.SourceCounts.SessionFiles)
	fmt.Printf("  session_queue_items: %d\n", report.SourceCounts.SessionQueueItems)
	fmt.Printf("  recent_cwds: %d\n", report.SourceCounts.RecentCwds)
	fmt.Printf("  cwd_groups: %d\n", report.SourceCounts.CwdGroups)
	fmt.Printf("  app_kv: %d\n", report.SourceCounts.AppKV)
	fmt.Printf("  legacy_import_unmapped: %d\n", report.SourceCounts.LegacyImportUnmapped)
	fmt.Printf("  session_key_union: %d\n", report.SourceCounts.SessionKeyUnion)
	fmt.Println("imported_counts:")
	fmt.Printf("  session_catalog_rows: %d\n", report.ImportedCounts.SessionCatalogRows)
	fmt.Printf("  session_source_ref_rows: %d\n", report.ImportedCounts.SessionSourceRefRows)
	fmt.Printf("  session_ui_state_merged_rows: %d\n", report.ImportedCounts.SessionUIStateMergedRows)
	fmt.Printf("  hidden_session_key_rows: %d\n", report.ImportedCounts.HiddenSessionKeyRows)
	fmt.Printf("  session_queue_item_rows: %d\n", report.ImportedCounts.SessionQueueItemRows)
	fmt.Printf("  session_file_mapped_rows: %d\n", report.ImportedCounts.SessionFileMappedRows)
	fmt.Printf("  session_file_skipped_rows: %d\n", report.ImportedCounts.SessionFileSkippedRows)
	fmt.Printf("  recent_cwd_rows: %d\n", report.ImportedCounts.RecentCwdRows)
	fmt.Printf("  cwd_group_rows: %d\n", report.ImportedCounts.CwdGroupRows)
	fmt.Printf("  app_kv_rows: %d\n", report.ImportedCounts.AppKVRows)
	fmt.Printf("  migration_warning_rows: %d\n", report.ImportedCounts.MigrationWarningRows)
	fmt.Printf("  import_provenance_rows: %d\n", report.ImportedCounts.ImportProvenanceRows)
	fmt.Println("validation:")
	fmt.Printf("  warning_count: %d\n", report.Validation.WarningCount)
	fmt.Printf("  unmapped_count: %d\n", report.Validation.UnmappedCount)
	fmt.Printf("  orphan_session_ui_state: %d\n", report.Validation.OrphanSessionUIState)
	fmt.Printf("  orphan_session_files: %d\n", report.Validation.OrphanSessionFiles)
	fmt.Printf("  orphan_session_queue_items: %d\n", report.Validation.OrphanSessionQueueItems)
	if len(report.Validation.Mismatches) > 0 {
		fmt.Println("mismatches:")
		for _, item := range report.Validation.Mismatches {
			fmt.Printf("  - %s: source=%d imported=%d", item.Name, item.Source, item.Imported)
			if strings.TrimSpace(item.Details) != "" {
				fmt.Printf(" detail=%s", item.Details)
			}
			fmt.Println()
		}
	}
	if len(report.SideJSON) > 0 {
		fmt.Println("side_json:")
		for _, item := range report.SideJSON {
			fmt.Printf("  - %s fresh=%t ignored=%t entries=%d mtime=%s", item.Name, item.Fresh, item.Ignored, item.EntryCount, item.ModifiedAt.UTC().Format(time.RFC3339))
			if strings.TrimSpace(item.ParseError) != "" {
				fmt.Printf(" parse_error=%s", item.ParseError)
			}
			fmt.Println()
		}
	}
}
