# Conventional Commits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document and automatically enforce Conventional Commits formatting for commit subjects and pull request titles.

**Architecture:** Keep the policy in a root contribution guide and enforce it in one pull-request workflow. The workflow checks the event title and all commit subjects in the pull request's base-to-head Git range with Bash.

**Tech Stack:** Markdown, GitHub Actions, Bash, Git

## Global Constraints

- Subjects and pull request titles use `<type>[optional scope][!]: <description>`.
- Types are lowercase.
- The workflow adds no project dependency and has only `contents: read` permission.
- Validation checks format, not whether the selected type is semantically correct.

---

### Task 1: Contribution guide

**Files:**
- Create: `CONTRIBUTING.md`

**Interfaces:**
- Consumes: Conventional Commits 1.0.0.
- Produces: The contributor-facing policy enforced by Task 2.

- [ ] **Step 1: Add the contribution guide**

```markdown
# Contributing

## Commit Messages and Pull Request Titles

Every commit subject and pull request title must follow
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

    <type>[optional scope][!]: <description>

- Use a lowercase noun for `type`. Use `feat` for a new feature and `fix` for a
  bug fix. Other common types include `build`, `chore`, `ci`, `docs`, `perf`,
  `refactor`, `style`, `test`, and `revert`.
- Use an optional scope in parentheses to identify the affected area.
- Write a short, non-empty description after the colon and space.
- Mark a breaking change with `!`. A commit may instead use a
  `BREAKING CHANGE: <description>` footer; a breaking pull request title must
  use `!`.

Valid examples:

    feat(slack): add alert thread replies
    fix: avoid duplicate notifications
    docs: explain local setup
    feat(api)!: remove the legacy endpoint

Invalid examples:

    Add alert thread replies
    Feat: add alert thread replies
    fix missing colon and space

Pull request titles are checked whenever a pull request is opened or edited.
All commit subjects in the pull request are checked whenever commits are pushed.
```

- [ ] **Step 2: Check Markdown and whitespace**

Run: `git diff --check && rg -n 'Conventional Commits|<type>|Pull request titles' CONTRIBUTING.md`

Expected: no whitespace errors and matches for the policy, format, and pull request rule.

- [ ] **Step 3: Commit**

```bash
git add CONTRIBUTING.md
git commit -m "docs: add contribution guidelines"
```

### Task 2: Pull request format check

**Files:**
- Create: `.github/workflows/conventional-commits.yml`

**Interfaces:**
- Consumes: `github.event.pull_request.title`, `base.sha`, `head.sha`, and the documented format.
- Produces: A check named `Conventional Commits / check`.

- [ ] **Step 1: Verify the invalid example fails**

Run:

```bash
bash -c 'pattern="^[a-z][a-z0-9-]*(\\([^()]+\\))?!?: .+$"; [[ "Add alerts" =~ $pattern ]]'
```

Expected: exit status 1.

- [ ] **Step 2: Add the workflow**

```yaml
name: Conventional Commits

on:
  pull_request:
    types: [opened, edited, reopened, synchronize]

permissions:
  contents: read

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: ${{ github.event.pull_request.head.sha }}

      - name: Check pull request title and commit subjects
        env:
          BASE_SHA: ${{ github.event.pull_request.base.sha }}
          HEAD_SHA: ${{ github.event.pull_request.head.sha }}
          PR_TITLE: ${{ github.event.pull_request.title }}
        shell: bash
        run: |
          pattern='^[a-z][a-z0-9-]*(\([^()]+\))?!?: .+'

          check() {
            local label=$1 message=$2
            if [[ ! $message =~ $pattern ]]; then
              echo "::error::$label does not follow Conventional Commits: $message"
              return 1
            fi
          }

          check "Pull request title" "$PR_TITLE"

          while IFS= read -r subject; do
            check "Commit subject" "$subject"
          done < <(git log --format=%s "$BASE_SHA..$HEAD_SHA")
```

- [ ] **Step 3: Exercise valid and invalid messages**

Run:

```bash
bash -c '
pattern="^[a-z][a-z0-9-]*(\\([^()]+\\))?!?: .+$"
valid=("feat: add alerts" "fix(slack): avoid duplicates" "feat(api)!: remove endpoint")
invalid=("Add alerts" "Feat: add alerts" "fix missing colon")
for message in "${valid[@]}"; do [[ $message =~ $pattern ]] || exit 1; done
for message in "${invalid[@]}"; do [[ ! $message =~ $pattern ]] || exit 1; done
'
```

Expected: exit status 0.

- [ ] **Step 4: Parse the workflow and inspect the diff**

Run:

```bash
ruby -e 'require "yaml"; YAML.load_file(ARGV.fetch(0))' .github/workflows/conventional-commits.yml
git diff --check
git diff -- CONTRIBUTING.md .github/workflows/conventional-commits.yml
```

Expected: YAML parses, whitespace is clean, and the diff matches the design.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/conventional-commits.yml
git commit -m "ci: check conventional commit style"
```

### Task 3: Final verification

**Files:**
- Verify: `CONTRIBUTING.md`
- Verify: `.github/workflows/conventional-commits.yml`

**Interfaces:**
- Consumes: The completed guide and workflow.
- Produces: Local validation evidence.

- [ ] **Step 1: Re-run all local checks**

```bash
git diff HEAD~2 --check
ruby -e 'require "yaml"; YAML.load_file(ARGV.fetch(0))' .github/workflows/conventional-commits.yml
bash -c '
pattern="^[a-z][a-z0-9-]*(\\([^()]+\\))?!?: .+$"
valid=("feat: add alerts" "fix(slack): avoid duplicates" "feat(api)!: remove endpoint")
invalid=("Add alerts" "Feat: add alerts" "fix missing colon")
for message in "${valid[@]}"; do [[ $message =~ $pattern ]] || exit 1; done
for message in "${invalid[@]}"; do [[ ! $message =~ $pattern ]] || exit 1; done
'
```

Expected: every command exits with status 0.

- [ ] **Step 2: Confirm commits follow the policy**

Run: `git log -3 --pretty=format:'%h %s'`

Expected: the plan, documentation, and workflow commits all use Conventional Commits subjects.
