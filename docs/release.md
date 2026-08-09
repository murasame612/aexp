# release.md — Release Workflow

`aexp` uses GitHub Releases with prebuilt binaries. The install path is:

```bash
curl -fsSL https://raw.githubusercontent.com/murasame612/aexp/main/scripts/install.sh | sh
```

The script downloads one of:

```text
aexp_darwin_amd64.tar.gz
aexp_darwin_arm64.tar.gz
aexp_linux_amd64.tar.gz
aexp_linux_arm64.tar.gz
checksums.txt
```

## Release a Version

1. Make sure tests pass:

   ```bash
   go test ./...
   ```

2. Tag and push:

   ```bash
   git tag v0.3.2
   git push origin v0.3.2
   ```

3. GitHub Actions runs `.github/workflows/release.yml`.

4. After the release is published, test the public installer:

   ```bash
   tmp="$(mktemp -d)"
   AEXP_INSTALL_DIR="$tmp/bin" \
     sh -c "$(curl -fsSL https://raw.githubusercontent.com/murasame612/aexp/main/scripts/install.sh)"
   "$tmp/bin/aexp" --version
   "$tmp/bin/aexp" mcp install --target codex --dry-run
   "$tmp/bin/aexp" mcp install --target hermes --dry-run
   ```

## Installer Overrides

```bash
AEXP_VERSION=v0.3.2 sh -c "$(curl -fsSL https://raw.githubusercontent.com/murasame612/aexp/main/scripts/install.sh)"
AEXP_REPO=owner/aexp sh -c "$(curl -fsSL https://raw.githubusercontent.com/owner/aexp/main/scripts/install.sh)"
AEXP_INSTALL_DIR=/usr/local/bin sh -c "$(curl -fsSL https://raw.githubusercontent.com/murasame612/aexp/main/scripts/install.sh)"
```

The installer intentionally does not modify MCP client config. Users opt into
that after install:

```bash
aexp mcp install --target all
```
