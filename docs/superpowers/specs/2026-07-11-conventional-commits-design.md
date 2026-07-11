# Conventional Commits Design

Date: 2026-07-11

## Goal

Require commit subjects and pull request titles to follow Conventional Commits 1.0.0.

## Changes

- Add a root `CONTRIBUTING.md` describing the required
  `<type>[optional scope][!]: <description>` format, common types, breaking
  changes, and examples.
- Add `.github/workflows/conventional-commits.yml` for pull requests.
- Run the check when a pull request is opened, reopened, synchronized, or its
  title is edited.
- Check the pull request title and every commit subject between the pull
  request base and head commits.

## Validation

Use `actions/checkout@v6` with full history and a small shell regular expression.
The workflow has only `contents: read` permission and adds no project dependency.

The check validates message shape and lowercase types. It does not attempt to
decide whether a change is semantically a feature, fix, or another type.

## Verification

Exercise the regular expression against valid and invalid examples and parse
the workflow as YAML before completion.

## Reference

- [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
