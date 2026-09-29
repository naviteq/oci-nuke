# Getting started

!!! warning "Dry-run is the default"
    A run with no `--no-dry-run` deletes nothing. Read the [safety model](index.md) before
    passing that flag for the first time.

--8<-- "README.md:install"

## Exit codes

Scripting around `oci-nuke` means reading its exit code, not its output.

--8<-- "README.md:exit-codes"
