<!--
Keep the description short; rationale that does not fit goes into the docs, linked from here.
One paragraph is one line: GitHub renders every newline as a line break, so never hard-wrap.
Link with short text, never a bare long URL.
-->

## What changed

<!-- What and why: a few bullets or one paragraph. -->

## Verification

<!-- What you ran and what it showed. -->

## Notes for reviewers

<!-- Optional: rollout, breaking change, follow-ups. Delete this section when there is nothing to say. -->

## If this PR adds or changes a resource type

<!-- Delete this section if it does not apply. -->

- [ ] The type is authored via `cmd/gen-resource` or hand-written following the same contract: a lister, `Filter()`, `Remove()`, `Properties()`, `GetCompartmentID()`, `UniqueKey()`, and `SafetyTags()`.
- [ ] `Filter()` is a present-state allow-list (never a deny-list of terminal states), verified against the pinned SDK's own lifecycle-state enum source.
- [ ] `DependsOn` is declared on the dependent type only, as bare string literals.
- [ ] List, Filter, and Remove tests are added against a hand-written stub client with zero network access.
- [ ] `go run ./tools/generate-docs` has been run and the resulting `docs/resources/<Type>.md` is committed.
- [ ] If the type has no lifecycle-state field, `Filter()` returns `nil` unconditionally, and the type has been added to `resources_test/filter_contract_test.go`'s reviewed allow-list with the SDK struct cited.

<!-- Keep Refs as the last line, naming the ticket in the branch name, e.g. "Refs: PROJ-123". Delete it when the branch names no ticket. -->
Refs:
