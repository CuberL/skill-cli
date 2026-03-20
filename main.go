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
	Managed   bool   `json:"managed"`
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
			return errors.New("usage: skill-cli add <repo-url-or-path>")
		}
		return addRepo(args[1])
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: skill-cli remove <repo-url-or-name>")
		}
		return removeRepo(args[1])
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
		return runList(args[1:])
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
		if !repo.Managed {
			fmt.Printf("skipping local repo: %s\n", repo.URL)
			continue
		}
		fmt.Printf("updating repo: %s\n", repo.URL)
		if err := pullRepo(repo.LocalPath); err != nil {
			return fmt.Errorf("update repo %s: %w", repo.URL, err)
		}
	}

	return syncAll(cfg)
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showAll := fs.Bool("all", false, "show installed skills under each repo")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	printConfig(cfg, *showAll)
	return nil
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
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: skill-cli target remove <path>")
		}
		return removeTarget(args[1])
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

	repoInput := strings.TrimSpace(repoURL)
	if repoInput == "" {
		return errors.New("repo url or path is required")
	}

	reference, isLocalPath, err := resolveRepoReference(repoInput)
	if err != nil {
		return err
	}

	if repo, found := findRepo(cfg, reference); found {
		fmt.Printf("repo already configured: %s\n", repo.URL)
		return syncAll(cfg)
	}

	repoName, err := deriveRepoName(reference)
	if err != nil {
		return err
	}

	localPath := reference
	managed := false
	if !isLocalPath {
		localPath, err = uniqueRepoPath(repoName)
		if err != nil {
			return err
		}
		if err := cloneRepo(reference, localPath); err != nil {
			return err
		}
		managed = true
	}

	cfg.Repos = append(cfg.Repos, RepoConfig{
		URL:       reference,
		Name:      repoName,
		LocalPath: localPath,
		Managed:   managed,
	})
	sortConfig(&cfg)

	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("added repo: %s\n", reference)
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

func removeRepo(repoFilter string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	repo, index, err := resolveSingleRepo(cfg, strings.TrimSpace(repoFilter))
	if err != nil {
		return err
	}

	skills, err := discoverSkills(repo.LocalPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("discover repo %s: %w", repo.URL, err)
	}

	for _, target := range cfg.Targets {
		if err := removeSkillsFromTarget(skills, target); err != nil {
			return fmt.Errorf("remove repo %s from %s: %w", repo.URL, target, err)
		}
	}

	if repo.Managed {
		if err := os.RemoveAll(repo.LocalPath); err != nil {
			return fmt.Errorf("remove local repo %s: %w", repo.LocalPath, err)
		}
	}

	cfg.Repos = append(cfg.Repos[:index], cfg.Repos[index+1:]...)
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("removed repo: %s\n", repo.URL)
	return syncAll(cfg)
}

func removeTarget(target string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	resolvedTarget, err := expandPath(target)
	if err != nil {
		return err
	}

	index := slices.Index(cfg.Targets, resolvedTarget)
	if index == -1 {
		return fmt.Errorf("target not found: %s", resolvedTarget)
	}

	for _, repo := range cfg.Repos {
		skills, err := discoverSkills(repo.LocalPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("discover repo %s: %w", repo.URL, err)
		}
		if err := removeSkillsFromTarget(skills, resolvedTarget); err != nil {
			return fmt.Errorf("remove repo %s from %s: %w", repo.URL, resolvedTarget, err)
		}
	}

	cfg.Targets = append(cfg.Targets[:index], cfg.Targets[index+1:]...)
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("removed target: %s\n", resolvedTarget)
	return nil
}

func syncAll(cfg Config) error {
	var errs []error
	repoRoots := collectRepoRoots(cfg.Repos)

	for _, target := range cfg.Targets {
		if err := cleanupTargetSymlinks(target, repoRoots); err != nil {
			errs = append(errs, fmt.Errorf("cleanup target %s: %w", target, err))
		}
	}

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

func collectRepoRoots(repos []RepoConfig) []string {
	repoRoots := make([]string, 0, len(repos))
	for _, repo := range repos {
		repoRoots = append(repoRoots, filepath.Clean(repo.LocalPath))
	}
	return repoRoots
}

func cleanupTargetSymlinks(target string, repoRoots []string) error {
	if len(repoRoots) == 0 {
		return nil
	}

	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	return filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}

		linkTarget, err := os.Readlink(path)
		if err != nil {
			return err
		}

		resolvedTarget := linkTarget
		if !filepath.IsAbs(resolvedTarget) {
			resolvedTarget = filepath.Join(filepath.Dir(path), resolvedTarget)
		}
		resolvedTarget = filepath.Clean(resolvedTarget)

		for _, repoRoot := range repoRoots {
			if isPathWithin(resolvedTarget, repoRoot) {
				return os.Remove(path)
			}
		}

		return nil
	})
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

func removeSkillsFromTarget(skills []string, target string) error {
	for _, skillPath := range skills {
		linkName := filepath.Join(target, filepath.Base(skillPath))
		if err := removeSymlinkIfMatches(skillPath, linkName); err != nil {
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

func removeSymlinkIfMatches(source, linkName string) error {
	info, err := os.Lstat(linkName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}

	existing, err := os.Readlink(linkName)
	if err != nil {
		return err
	}

	existingAbs := existing
	if !filepath.IsAbs(existingAbs) {
		existingAbs = filepath.Join(filepath.Dir(linkName), existingAbs)
	}
	existingAbs = filepath.Clean(existingAbs)

	if existingAbs != filepath.Clean(source) {
		resolved, resolveErr := filepath.EvalSymlinks(linkName)
		if resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(source) {
			return nil
		}
	}

	return os.Remove(linkName)
}

func discoverSkills(repoRoot string) ([]string, error) {
	var discovered []string

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
		discovered = append(discovered, filepath.Clean(filepath.Dir(path)))
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(discovered)

	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, skillDir := range discovered {
		discoveredSet[skillDir] = struct{}{}
	}

	var skills []string
	seen := map[string]struct{}{}
	repoRoot = filepath.Clean(repoRoot)

	for _, skillDir := range discovered {
		if isSubSkill(repoRoot, skillDir, discoveredSet) {
			continue
		}
		if _, ok := seen[filepath.Base(skillDir)]; ok {
			return nil, fmt.Errorf("duplicate skill directory name detected: %s", filepath.Base(skillDir))
		}
		seen[filepath.Base(skillDir)] = struct{}{}
		skills = append(skills, skillDir)
	}

	slices.Sort(skills)
	return skills, nil
}

func isSubSkill(repoRoot, skillDir string, discoveredSet map[string]struct{}) bool {
	parent := filepath.Dir(skillDir)
	for {
		if parent == skillDir || parent == "." {
			return false
		}
		if _, ok := discoveredSet[parent]; ok {
			return true
		}
		if parent == repoRoot {
			return false
		}
		next := filepath.Dir(parent)
		if next == parent {
			return false
		}
		parent = next
	}
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
		if repo.URL == repoURL || repo.LocalPath == repoURL {
			return repo, true
		}
	}
	return RepoConfig{}, false
}

func resolveSingleRepo(cfg Config, repoFilter string) (RepoConfig, int, error) {
	for i, repo := range cfg.Repos {
		if repo.URL == repoFilter || repo.Name == repoFilter || repo.LocalPath == repoFilter {
			return repo, i, nil
		}
	}

	return RepoConfig{}, -1, fmt.Errorf("repo not found: %s", repoFilter)
}

func filterRepos(cfg Config, repoFilter string) ([]RepoConfig, error) {
	if repoFilter == "" {
		return cfg.Repos, nil
	}

	var matches []RepoConfig
	for _, repo := range cfg.Repos {
		if repo.URL == repoFilter || repo.Name == repoFilter || repo.LocalPath == repoFilter {
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

	for i := range cfg.Repos {
		if cfg.Repos[i].Managed {
			continue
		}
		managed, err := inferManagedRepo(cfg.Repos[i].LocalPath)
		if err != nil {
			return Config{}, err
		}
		cfg.Repos[i].Managed = managed
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

func printConfig(cfg Config, showAll bool) {
	fmt.Println("repos:")
	for _, repo := range cfg.Repos {
		switch {
		case !repo.Managed && repo.URL == repo.LocalPath:
			fmt.Printf("- %s (local)\n", repo.URL)
		default:
			fmt.Printf("- %s (%s)\n", repo.URL, repo.LocalPath)
		}
		if !showAll {
			continue
		}
		skills, err := discoverSkills(repo.LocalPath)
		if err != nil {
			fmt.Printf("  - error: %v\n", err)
			continue
		}
		if len(skills) == 0 {
			fmt.Println("  - skills: none")
			continue
		}
		for _, skillPath := range skills {
			fmt.Printf("  - %s\n", filepath.Base(skillPath))
		}
	}
	fmt.Println("targets:")
	for _, target := range cfg.Targets {
		fmt.Printf("- %s\n", target)
	}
}

func printUsage() {
	fmt.Println(`skill-cli

Usage:
  skill-cli add <repo-url-or-path>
  skill-cli remove <repo-url-or-name>
  skill-cli target add <path>
  skill-cli target remove <path>
  skill-cli target list
  skill-cli list [--all]
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

func resolveRepoReference(value string) (string, bool, error) {
	expanded, err := expandPath(value)
	if err == nil {
		info, statErr := os.Stat(expanded)
		if statErr == nil {
			if !info.IsDir() {
				return "", false, fmt.Errorf("local path is not a directory: %s", expanded)
			}
			return expanded, true, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", false, statErr
		}
	}

	return value, false, nil
}

func inferManagedRepo(localPath string) (bool, error) {
	repoRoot, err := reposRoot()
	if err != nil {
		return false, err
	}

	rel, err := filepath.Rel(repoRoot, localPath)
	if err != nil {
		return false, nil
	}
	if rel == "." {
		return true, nil
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..", nil
}

func isPathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)

	if path == root {
		return true
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
