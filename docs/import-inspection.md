# Inspect a source package

Inspect the structure and original-file hashes of a reconciled source package:

```sh
vult-server-admin inspect-import --archive company.vult-import.zip --output inspection.json
```

This offline command uses no database credential or network access. It preserves
the input, never extracts files, and creates a new private report without
overwriting an existing path. Filenames and source contents stay out of errors
and status messages.

The supported container is a single-disk ZIP with `source.json` and SHA-256-named
`blobs/` members. The JSON has schema 1 and retains its original account, record,
Book and artifact objects. Unknown source fields remain part of the original
bytes. Each declared artifact must have its matching blob; repeated references
to the same bytes are valid. Extra, missing, mismatched and ambiguous members are
rejected. Archive comments, encrypted, ZIP64, symbolic-link, special-file and executable-mode
members are unsupported. Original blob contents remain opaque data.

Limits are 128 MiB compressed, 256 MiB expanded, 4,096 members, 16 MiB source JSON,
32 MiB per blob and 16,384 artifact references. Validation checks the central
directory before allocating the ZIP member list and honours cancellation.

The report identifies the exact archive and JSON hashes, verified blob count and
expanded size. Its `accounting_status` stays `not_checked`. Financial comparisons,
source identities, mapping decisions, coverage and accepted imports require
their separate accounting preview and authorization.
