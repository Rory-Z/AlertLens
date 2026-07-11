# Contributing

## Commit Messages and Pull Request Titles

Every commit subject and pull request title must follow
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```text
<type>[optional scope][!]: <description>
```

- Use a lowercase noun for `type`. Use `feat` for a new feature and `fix` for a
  bug fix. Other common types include `build`, `chore`, `ci`, `docs`, `perf`,
  `refactor`, `style`, `test`, and `revert`.
- Use an optional scope in parentheses to identify the affected area.
- Write a short, non-empty description after the colon and space.
- Mark a breaking change with `!`. A commit may instead use a
  `BREAKING CHANGE: <description>` footer; a breaking pull request title must
  use `!`.

Valid examples:

```text
feat(slack): add alert thread replies
fix: avoid duplicate notifications
docs: explain local setup
feat(api)!: remove the legacy endpoint
```

Invalid examples:

```text
Add alert thread replies
Feat: add alert thread replies
fix missing colon and space
```

Pull request titles are checked whenever a pull request is opened or edited.
All commit subjects in the pull request are checked whenever commits are pushed.
