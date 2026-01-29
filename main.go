package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

type Agent struct {
	Description string `yaml:"description" json:"description"`
	Prompt      string `yaml:"prompt" json:"prompt"`
}

type Config struct {
	Name      string            `yaml:"name"`
	Prompt    string            `yaml:"prompt"`
	Args      []string          `yaml:"args"`
	Agents    map[string]Agent  `yaml:"agents"`
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "unum")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unum")
}

func configPath(persona string) string {
	return filepath.Join(configDir(), persona+".yaml")
}

func cacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "unum")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "unum")
}

func sessionDir(persona, workDir string) string {
	// Convert /home/dev/Projects/foo to home-dev-Projects-foo
	dasherized := strings.ReplaceAll(strings.TrimPrefix(workDir, "/"), "/", "-")
	return filepath.Join(cacheDir(), persona, dasherized)
}

func loadConfig(persona string) (*Config, error) {
	data, err := os.ReadFile(configPath(persona))
	if err != nil {
		return nil, fmt.Errorf("config not found: %s (run 'unum %s init' to create)", configPath(persona), persona)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

func writeTemplate(persona string) error {
	path := configPath(persona)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	}

	if err := os.MkdirAll(configDir(), 0755); err != nil {
		return err
	}

	template := fmt.Sprintf(`name: %s
prompt: |
  # %s

  You are %s. Define your persona here.

  ## Working Directory

  Your working directory is {{.WorkDir}}.
  Before your first tool use, run: cd {{.WorkDir}}
args:
  - "--model"
  - "sonnet"
# agents:
#   worker:
#     description: "A helper agent"
#     prompt: "You are a helpful assistant"
`, persona, persona, persona)

	if err := os.WriteFile(path, []byte(template), 0644); err != nil {
		return err
	}

	fmt.Printf("Created %s\n", path)
	return nil
}

func invoke(persona string, extraArgs []string) error {
	cfg, err := loadConfig(persona)
	if err != nil {
		return err
	}

	// Get current working directory
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}

	// Create persistent session directory (enables --continue and --resume)
	sessDir := sessionDir(persona, workDir)
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		return err
	}

	// Expand template variables in prompt
	prompt := os.Expand(cfg.Prompt, func(key string) string {
		switch key {
		case "WorkDir":
			return workDir
		default:
			return "$" + key // preserve unknown variables
		}
	})
	// Also handle {{.WorkDir}} style
	prompt = replaceTemplate(prompt, "{{.WorkDir}}", workDir)

	// Build claude args
	args := []string{
		"--system-prompt", prompt,
		"--add-dir", workDir,
	}

	// Add agents if defined
	if len(cfg.Agents) > 0 {
		agentsJSON, err := json.Marshal(cfg.Agents)
		if err != nil {
			return fmt.Errorf("failed to marshal agents: %w", err)
		}
		args = append(args, "--agents", string(agentsJSON))
	}

	// Add user-defined args from config
	args = append(args, cfg.Args...)

	// Add extra args from command line
	args = append(args, extraArgs...)

	// Find claude binary
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH")
	}

	// Change to session directory and exec claude
	if err := os.Chdir(sessDir); err != nil {
		return err
	}

	// Exec replaces the current process
	return syscall.Exec(claudePath, append([]string{"claude"}, args...), os.Environ())
}

func replaceTemplate(s, old, new string) string {
	result := s
	for {
		i := indexOf(result, old)
		if i < 0 {
			break
		}
		result = result[:i] + new + result[i+len(old):]
	}
	return result
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func usage() {
	fmt.Fprintf(os.Stderr, `unum - persona launcher for claude code

Usage:
  unum <persona> [flags...]   Launch claude with the specified persona
  unum <persona> init         Create a template config for the persona

Flags are passed through to claude (e.g., --continue, --resume, -p "prompt")

Config files are stored in ~/.config/unum/<persona>.yaml
`)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	persona := os.Args[1]

	// Handle help flags
	if persona == "-h" || persona == "--help" || persona == "help" {
		usage()
	}

	if len(os.Args) >= 3 && os.Args[2] == "init" {
		if err := writeTemplate(persona); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Everything after persona is passed through to claude
	extraArgs := os.Args[2:]

	if err := invoke(persona, extraArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
