package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	appDirName  = ".skill-cli"
	configFile  = "config.json"
	reposDir    = "repos"
	defaultPerm = 0o755
)

type Config struct {
	Repos   []RepoConfig `json:"repos"`
	Targets []string     `json:"targets"`
}

type RepoConfig struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	LocalPath string `json:"local_path"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "add":
		if len(args) != 2 {
			return errors.New("usage: skill-cli add <repo-url>")
		}
		return addRepo(args[1])
	case "target":
		return runTarget(args[1:])
	case "sync":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return syncAll(cfg)
	case "update":
		return runUpdate(args[1:])
	case "list":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		printConfig(cfg)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repoFilter := fs.String("repo", "", "repo url or configured repo name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	repos, err := filterRepos(cfg, strings.TrimSpace(*repoFilter))
	if err != nil {
		return err
	}

	for _, repo := range repos {
		fmt.Printf("updating repo: %s\n", repo.URL)
		if err := pullRepo(repo.LocalPath); err != nil {
			return fmt.Errorf("update repo %s: %w", repo.URL, err)
		}
	}

	return syncAll(cfg)
}

func runTarget(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: skill-cli target <add|list> [...]")
	}

	switch args[0] {
	case "add":
		if len(args) != 2 {
			return errors.New("usage: skill-cli target add <path>")
		}
		return addTarget(args[1])
	case "list":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		for _, target := range cfg.Targets {
			fmt.Println(target)
		}
		return nil
	default:
		return fmt.Errorf("unknown target command %q", args[0])
	}
}

func addRepo(repoURL string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	normalizedURL := strings.TrimSpace(repoURL)
	if normalizedURL == "" {
		return errors.New("repo url is required")
	}

	if repo, found := findRepo(cfg, normalizedURL); found {
		fmt.Printf("repo already configured: %s\n", repo.URL)
		return syncAll(cfg)
	}

	repoName, err := deriveRepoName(normalizedURL)
	if err != nil {
		return err
	}

	localPath, err := uniqueRepoPath(repoName)
	if err != nil {
		return err
	}

	if err := cloneRepo(normalizedURL, localPath); err != nil {
		return err
	}

	cfg.Repos = append(cfg.Repos, RepoConfig{
		URL:       normalizedURL,
		Name:      repoName,
		LocalPath: localPath,
	})
	sortConfig(&cfg)

	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("added repo: %s\n", normalizedURL)
	return syncAll(cfg)
}

func addTarget(target string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	resolvedTarget, err := expandPath(target)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(resolvedTarget, defaultPerm); err != nil {
		return fmt.Errorf("create target: %w", err)
	}

	if !slices.Contains(cfg.Targets, resolvedTarget) {
		cfg.Targets = append(cfg.Targets, resolvedTarget)
		sortConfig(&cfg)
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("added target: %s\n", resolvedTarget)
	} else {
		fmt.Printf("target already configured: %s\n", resolvedTarget)
	}

	return syncAll(cfg)
}

func syncAll(cfg Config) error {
	var errs []error
	for _, repo := range cfg.Repos {
		skills, err := discoverSkills(repo.LocalPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("discover repo %s: %w", repo.URL, err))
			continue
		}
		for _, target := range cfg.Targets {
			if err := syncRepoToTarget(skills, target); err != nil {
				errs = append(errs, fmt.Errorf("sync repo %s to %s: %w", repo.URL, target, err))
			}
		}
	}

	if len(errs) == 0 {
		fmt.Println("sync complete")
		return nil
	}

	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "sync warning: %v\n", err)
	}
	return errors.New("sync completed with warnings")
}

func syncRepoToTarget(skills []string, target string) error {
	if err := os.MkdirAll(target, defaultPerm); err != nil {
		return err
	}

	for _, skillPath := range skills {
		linkName := filepath.Join(target, filepath.Base(skillPath))
		if err := ensureSymlink(skillPath, linkName); err != nil {
			return err
		}
	}

	return nil
}

func ensureSymlink(source, linkName string) error {
	info, err := os.Lstat(linkName)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			existing, readErr := os.Readlink(linkName)
			if readErr != nil {
				return readErr
			}
			existingAbs, readErr := filepath.EvalSymlinks(linkName)
			if readErr == nil && existingAbs == source {
				return nil
			}
			if filepath.Clean(existing) == filepath.Clean(source) {
				return nil
			}
			return fmt.Errorf("path already exists as symlink to %s", existing)
		}
		return fmt.Errorf("path already exists and is not a symlink: %s", linkName)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return os.Symlink(source, linkName)
}

func discoverSkills(repoRoot string) ([]string, error) {
	var skills []string
	seen := map[string]struct{}{}

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		skillDir := filepath.Dir(path)
		if _, ok := seen[filepath.Base(skillDir)]; ok {
			return fmt.Errorf("duplicate skill directory name detected: %s", filepath.Base(skillDir))
		}
		seen[filepath.Base(skillDir)] = struct{}{}
		skills = append(skills, skillDir)
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(skills)
	return skills, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

func cloneRepo(repoURL, localPath string) error {
	repoParent := filepath.Dir(localPath)
	if err := os.MkdirAll(repoParent, defaultPerm); err != nil {
		return fmt.Errorf("prepare repo dir: %w", err)
	}

	cmd := exec.Command("git", "clone", repoURL, localPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

func pullRepo(localPath string) error {
	cmd := exec.Command("git", "-C", localPath, "pull", "--ff-only")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git pull failed: %w", err)
	}
	return nil
}

func uniqueRepoPath(repoName string) (string, error) {
	repoRoot, err := reposRoot()
	if err != nil {
		return "", err
	}

	candidate := filepath.Join(repoRoot, repoName)
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}

	for i := 2; ; i++ {
		candidate = filepath.Join(repoRoot, fmt.Sprintf("%s-%d", repoName, i))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
}

func deriveRepoName(rawURL string) (string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(rawURL), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")

	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	pos := max(lastSlash, lastColon)
	if pos >= 0 && pos < len(trimmed)-1 {
		return sanitizeName(trimmed[pos+1:]), nil
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" {
			return sanitizeName(base), nil
		}
	}

	return "", fmt.Errorf("unable to derive repo name from %q", rawURL)
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, string(filepath.Separator), "-")
	if name == "" {
		return "repo"
	}
	return name
}

func findRepo(cfg Config, repoURL string) (RepoConfig, bool) {
	for _, repo := range cfg.Repos {
		if repo.URL == repoURL {
			return repo, true
		}
	}
	return RepoConfig{}, false
}

func filterRepos(cfg Config, repoFilter string) ([]RepoConfig, error) {
	if repoFilter == "" {
		return cfg.Repos, nil
	}

	var matches []RepoConfig
	for _, repo := range cfg.Repos {
		if repo.URL == repoFilter || repo.Name == repoFilter {
			matches = append(matches, repo)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("repo not found: %s", repoFilter)
	}

	return matches, nil
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	sortConfig(&cfg)
	return cfg, nil
}

func saveConfig(cfg Config) error {
	sortConfig(&cfg)
	root, err := appRoot()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(root, defaultPerm); err != nil {
		return fmt.Errorf("create app dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	path, err := configPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func sortConfig(cfg *Config) {
	slices.Sort(cfg.Targets)
	slices.SortFunc(cfg.Repos, func(a, b RepoConfig) int {
		return strings.Compare(a.URL, b.URL)
	})
}

func printConfig(cfg Config) {
	fmt.Println("repos:")
	for _, repo := range cfg.Repos {
		fmt.Printf("- %s (%s)\n", repo.URL, repo.LocalPath)
	}
	fmt.Println("targets:")
	for _, target := range cfg.Targets {
		fmt.Printf("- %s\n", target)
	}
}

func printUsage() {
	fmt.Println(`skill-cli

Usage:
  skill-cli add <repo-url>
  skill-cli target add <path>
  skill-cli target list
  skill-cli list
  skill-cli sync
  skill-cli update [--repo <repo-url-or-name>]`)
}

func configPath() (string, error) {
	root, err := appRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, configFile), nil
}

func reposRoot() (string, error) {
	root, err := appRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, reposDir), nil
}

func appRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, appDirName), nil
}

func expandPath(value string) (string, error) {
	if value == "" {
		return "", errors.New("path is required")
	}

	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		switch value {
		case "~":
			value = home
		default:
			if strings.HasPrefix(value, "~/") {
				value = filepath.Join(home, value[2:])
			}
		}
	}

	return filepath.Abs(value)
}
