# Contributing

## Testing

Feature changes and bug fixes follow test-driven development:

1. Add an integration test that fails for the intended behavior.
2. Make the smallest production change that passes the test.
3. Refactor while keeping the test suite green.

AlertLens follows the testing trophy, with integration tests providing most
behavioral coverage:

- Integration tests assemble real production components, replace only
  out-of-process network transports or external systems, and assert observable
  behavior instead of mocking internal implementation.
- Every behavior change must have integration coverage. Unit tests are reserved
  for pure boundary logic such as parsing, validation, sanitization, and
  serialization; they do not replace tests of component collaboration.
- Contract tests protect external protocols and payloads. Opt-in real-system E2E
  tests cover only a small number of critical cross-system paths.

Before submitting a change, run the race-enabled test suite and inspect total
statement coverage:

```bash
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

CI rejects total statement coverage below 90%. Do not lower the threshold or
exclude code from coverage to make a change pass.

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
