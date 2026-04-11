// Phalanx CLI — command-line tool for managing reviews, skills, agents, and audit.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var serverURL string
var apiToken string

func main() {
	root := &cobra.Command{
		Use:   "phalanx",
		Short: "AI-powered pull request review platform",
	}

	root.PersistentFlags().StringVar(&serverURL, "server", os.Getenv("PHALANX_URL"), "Phalanx server URL")
	root.PersistentFlags().StringVar(&apiToken, "token", os.Getenv("PHALANX_TOKEN"), "API token")

	// --- review ---
	reviewCmd := &cobra.Command{
		Use:   "review",
		Short: "Trigger a Phalanx review for the current PR",
		RunE:  runReview,
	}
	reviewCmd.Flags().String("repo", "", "Repository (owner/name)")
	reviewCmd.Flags().Int("pr", 0, "PR number")
	reviewCmd.Flags().String("platform", "github", "Platform: github or gitlab")
	reviewCmd.Flags().String("head", "", "Head SHA")
	reviewCmd.Flags().String("base", "", "Base SHA")
	reviewCmd.Flags().Bool("wait", false, "Wait for completion")
	reviewCmd.Flags().Int("timeout", 120, "Timeout in seconds")
	reviewCmd.Flags().String("fail-on", "fail", "Exit non-zero on verdict")
	reviewCmd.Flags().String("output", "", "Write report to file")
	root.AddCommand(reviewCmd)

	// --- skill register ---
	skillCmd := &cobra.Command{Use: "skill", Short: "Manage skills"}
	skillRegister := &cobra.Command{
		Use:   "register [file]",
		Short: "Register a skill from YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var skill struct {
				Slug              string   `yaml:"slug" json:"slug"`
				Name              string   `yaml:"name" json:"name"`
				Version           int      `yaml:"version" json:"version"`
				SystemPrompt      string   `yaml:"system_prompt" json:"systemPrompt"`
				ChecklistTemplate string   `yaml:"checklist_template" json:"checklistTemplate"`
				Tags              []string `yaml:"tags" json:"tags"`
				OutputSchema      any      `yaml:"output_schema" json:"outputSchema,omitempty"`
			}
			if err := yaml.Unmarshal(data, &skill); err != nil {
				return fmt.Errorf("invalid YAML: %w", err)
			}
			if skill.Version == 0 {
				skill.Version = 1
			}
			jsonBody, err := json.Marshal(skill)
			if err != nil {
				return err
			}
			resp, err := apiPost("/api/skills", jsonBody)
			if err != nil {
				return err
			}
			fmt.Printf("✅ Skill registered (%s v%d): %s\n", skill.Slug, skill.Version, resp)
			return nil
		},
	}
	skillList := &cobra.Command{
		Use:   "list",
		Short: "List all skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := apiGet("/api/skills")
			if err != nil {
				return err
			}
			fmt.Println(body)
			return nil
		},
	}
	skillCmd.AddCommand(skillRegister, skillList)
	root.AddCommand(skillCmd)

	// --- agent ---
	agentCmd := &cobra.Command{Use: "agent", Short: "Manage agents"}
	agentList := &cobra.Command{
		Use: "list", Short: "List all agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := apiGet("/api/agents")
			if err != nil {
				return err
			}
			fmt.Println(body)
			return nil
		},
	}
	agentCmd.AddCommand(agentList)
	root.AddCommand(agentCmd)

	// --- audit ---
	auditCmd := &cobra.Command{Use: "audit", Short: "Query audit trail"}
	auditTrail := &cobra.Command{
		Use: "trail [sessionId]", Short: "View session audit trail",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := apiGet("/api/audit/session/" + args[0])
			if err != nil {
				return err
			}
			fmt.Println(body)
			return nil
		},
	}
	auditVerify := &cobra.Command{
		Use: "verify", Short: "Verify hash chain",
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := apiGet("/api/audit/verify")
			if err != nil {
				return err
			}
			fmt.Println(body)
			return nil
		},
	}
	auditCmd.AddCommand(auditTrail, auditVerify)
	root.AddCommand(auditCmd)

	// --- status ---
	root.AddCommand(&cobra.Command{
		Use: "status [sessionId]", Short: "Check review session status",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := apiGet("/api/reviews/" + args[0])
			if err != nil {
				return err
			}
			fmt.Println(body)
			return nil
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runReview(cmd *cobra.Command, args []string) error {
	repo, _ := cmd.Flags().GetString("repo")
	pr, _ := cmd.Flags().GetInt("pr")
	plat, _ := cmd.Flags().GetString("platform")
	head, _ := cmd.Flags().GetString("head")
	base, _ := cmd.Flags().GetString("base")
	wait, _ := cmd.Flags().GetBool("wait")
	timeout, _ := cmd.Flags().GetInt("timeout")
	failOn, _ := cmd.Flags().GetString("fail-on")
	output, _ := cmd.Flags().GetString("output")

	// Auto-detect from CI
	if os.Getenv("GITHUB_ACTIONS") != "" {
		plat = "github"
		if repo == "" { repo = os.Getenv("GITHUB_REPOSITORY") }
		if head == "" { head = os.Getenv("GITHUB_SHA") }
	}
	if os.Getenv("GITLAB_CI") != "" {
		plat = "gitlab"
		if repo == "" { repo = os.Getenv("CI_PROJECT_PATH") }
		if head == "" { head = os.Getenv("CI_COMMIT_SHA") }
	}

	// Git fallback
	if head == "" {
		out, _ := exec.Command("git", "rev-parse", "HEAD").Output()
		head = strings.TrimSpace(string(out))
	}
	if base == "" {
		out, _ := exec.Command("git", "merge-base", "HEAD", "origin/main").Output()
		base = strings.TrimSpace(string(out))
	}

	fmt.Printf("🛡️  Phalanx Review\n   Repo: %s\n   PR: #%d\n   %s..%s\n\n", repo, pr, base[:7], head[:7])

	// Trigger
	body := fmt.Sprintf(`{"platform":"%s","repository":"%s","prNumber":%d,"headSha":"%s","baseSha":"%s","triggerSource":"cli"}`,
		plat, repo, pr, head, base)

	resp, err := apiPostRaw("/api/reviews", body)
	if err != nil {
		return fmt.Errorf("trigger failed: %w", err)
	}

	var triggerResp struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal([]byte(resp), &triggerResp)
	fmt.Printf("✅ Session: %s\n", triggerResp.SessionID)

	if !wait {
		return nil
	}

	// Poll
	fmt.Println("⏳ Waiting...")
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)

	for time.Now().Before(deadline) {
		statusBody, _ := apiGet("/api/reviews/" + triggerResp.SessionID)
		var status struct {
			Session struct {
				Status          string  `json:"status"`
				OverallVerdict  *string `json:"overall_verdict"`
				CompositeReport *string `json:"composite_report"`
			} `json:"session"`
		}
		json.Unmarshal([]byte(statusBody), &status)

		if status.Session.Status == "completed" || status.Session.Status == "failed" {
			verdict := "unknown"
			if status.Session.OverallVerdict != nil {
				verdict = *status.Session.OverallVerdict
			}
			fmt.Printf("\n📋 Verdict: %s\n", strings.ToUpper(verdict))

			if output != "" && status.Session.CompositeReport != nil {
				os.WriteFile(output, []byte(*status.Session.CompositeReport), 0644)
				fmt.Printf("📝 Report: %s\n", output)
			}

			if failOn == "fail" && verdict == "fail" {
				os.Exit(1)
			}
			if failOn == "warn" && (verdict == "fail" || verdict == "warn") {
				os.Exit(1)
			}
			return nil
		}

		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("timed out after %ds", timeout)
}

func apiGet(path string) (string, error) {
	req, _ := http.NewRequest("GET", serverURL+path, nil)
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func apiPost(path string, data []byte) (string, error) {
	return apiPostRaw(path, string(data))
}

func apiPostRaw(path string, data string) (string, error) {
	req, _ := http.NewRequest("POST", serverURL+path, strings.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+apiToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}
