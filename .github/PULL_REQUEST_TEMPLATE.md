## What does this PR do

<!-- Free-text description of the change and why it's needed. -->

## If this PR adds or changes a resource type

<!-- Delete this section if it does not apply. -->

- [ ] The type is authored via `cmd/gen-resource` or hand-written following the same contract:
      a lister, `Filter()`, `Remove()`, `Properties()`, `GetCompartmentID()`, `UniqueKey()`, and
      `SafetyTags()`.
- [ ] `Filter()` is a present-state allow-list (never a deny-list of terminal states), verified
      against the pinned SDK's own lifecycle-state enum source.
- [ ] `DependsOn` is declared on the dependent type only, as bare string literals.
- [ ] List, Filter, and Remove tests are added against a hand-written stub client with zero
      network access.
- [ ] `go run ./tools/generate-docs` has been run and the resulting `docs/resources/<Type>.md`
      is committed.
- [ ] If the type has no lifecycle-state field, `Filter()` returns `nil` unconditionally, and the
      type has been added to `resources_test/filter_contract_test.go`'s reviewed allow-list with
      the SDK struct cited.
