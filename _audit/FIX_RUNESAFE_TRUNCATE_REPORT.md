# FIX_RUNESAFE_TRUNCATE_REPORT

## Summary
- Converted the targeted string truncation paths from byte slicing to rune-safe slicing.
- Preserved existing numeric limits (200/400/1000/2000 call-site values unchanged), with `max` now interpreted as rune count for the changed helpers.
- Added focused tests for English, Chinese, and mixed strings to prevent UTF-8 boundary corruption.

## Changed Files
- `bot/internal/bot/moderation.go`: `truncateString` now uses `utf8.RuneCountInString` and `[]rune` slicing.
- `bot/internal/api/routes_admin.go`: `truncateProfileCheckLogBio` now counts and slices runes while preserving the trailing `…` behavior.
- `bot/internal/worker/llm_prober.go`: `truncateError` now rune-safely truncates before appending `…`.
- `bot/internal/bot/truncate_test.go`: covers English, Chinese, and mixed truncation for `truncateString`.
- `bot/internal/api/truncate_test.go`: covers English, Chinese, and mixed truncation for `truncateProfileCheckLogBio`.

## Risk
- `max` semantics changed from byte count to rune count, which effectively expands capacity by roughly 3x for common CJK text.
- This is intentional for user/admin-facing fields and PostgreSQL `TEXT` columns have no length cap here.
- The worker prober alert error text also now uses character count instead of byte count, so non-ASCII diagnostic text can be longer in bytes while remaining valid UTF-8.

## Validation
- `cd bot && go build ./... && go vet ./... && go test ./... -count=1 -race -timeout 240s`
- Result: PASS

## Commit
- Pending at report creation time; see final handoff for commit hash.
