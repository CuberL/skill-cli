# skill-cli

`skill-cli` is a command-line tool for managing shared skill repositories and syncing them into multiple AI tool targets with symlinks.

## Features

- Add a skill repository once and reuse it across multiple targets
- Clone repositories locally on first add
- Sync all discovered skills into Codex, Claude Code, Cursor, or any other target directory
- Update all configured repositories with one command
- Update a single repository with `--repo`

## Requirements

- Go 1.22 or later
- curl
- tar

## Installation

Install with `curl | bash`:

```bash
curl -fsSL https://raw.githubusercontent.com/CuberL/skill-cli/main/install.sh | bash
```

Install to a custom directory:

```bash
curl -fsSL https://raw.githubusercontent.com/CuberL/skill-cli/main/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
```

Install a specific tag:

```bash
curl -fsSL https://raw.githubusercontent.com/CuberL/skill-cli/main/install.sh | SKILL_CLI_VERSION="v0.1.0" bash
```

By default, the installer uses `/usr/local/bin` when writable, otherwise it falls back to `~/.local/bin`.

## Usage

Add a repository:

```bash
skill-cli add https://github.com/example/skills.git
```

Add targets:

```bash
skill-cli target add ~/.codex/skill
skill-cli target add ~/.cursor/skills
```

List current configuration:

```bash
skill-cli list
skill-cli target list
```

Sync all configured repositories to all targets:

```bash
skill-cli sync
```

Update all repositories:

```bash
skill-cli update
```

Update a single repository:

```bash
skill-cli update --repo my-skills
skill-cli update --repo https://github.com/example/skills.git
```

## Typical Workflow

```bash
skill-cli add https://github.com/example/team-skills.git
skill-cli target add ~/.codex/skill
skill-cli target add ~/.cursor/skills
skill-cli update
```

## Notes

- `skill-cli add` automatically syncs the new repository to all existing targets.
- `skill-cli target add` automatically syncs all configured repositories to the new target.
- If a target already contains a non-symlink path with the same skill name, `skill-cli` will report a conflict instead of overwriting it.
