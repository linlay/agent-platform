# Vendored ripgrep

This directory stores prebuilt `rg` binaries used by the program release bundle.

- Upstream: https://github.com/BurntSushi/ripgrep
- Version: 15.1.0
- Licenses: MIT or Unlicense, copied into each vendored version directory.

Update the binaries with:

```bash
RIPGREP_VERSION=15.1.0 scripts/vendor-ripgrep.sh
```

The release scripts look for binaries at:

```text
third_party/ripgrep/<version>/<os>-<arch>/rg
third_party/ripgrep/<version>/<os>-<arch>/rg.exe
```
