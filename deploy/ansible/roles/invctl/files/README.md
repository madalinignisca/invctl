<!--
invctl — infrastructure inventory
Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>

Licensed under the GNU Affero General Public License, version 3 only —
no later version applies. See LICENSE for the full text.

SPDX-License-Identifier: AGPL-3.0-only
-->

# Offline binaries go here

Set `invctl_source: files` and put **exactly these two files** in this
directory, with exactly these names, where `<version>` is `invctl_version` from
`defaults/main.yml`:

```
invctl_<version>_linux_amd64
invctl_<version>_checksums.txt
```

For 1.2.0 that is `invctl_1.2.0_linux_amd64` and
`invctl_1.2.0_checksums.txt`. Download both from

```
https://github.com/madalinignisca/invctl/releases/download/v1.2.0/
```

on a machine that can reach it, and carry them in.

**Both files. The checksum is verified here exactly as it is on the online
path** — the offline path exists because a segmented environment cannot reach
GitHub, not because integrity checking is inconvenient there. A binary carried
in on a laptop has had more hands on it than one fetched over TLS, not fewer.

Do not add other files to the checksums file or rename the binary. The role
requires exactly one record naming exactly `invctl_<version>_linux_amd64` and
refuses when it finds none or more than one.

Neither file is committed: `.gitignore` here keeps a 20 MB binary out of the
repository's history, where it would be permanent.
