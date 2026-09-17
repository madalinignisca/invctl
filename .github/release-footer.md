
---

## Install

One static binary, no runtime dependencies. Linux x86-64.

```bash
curl -LO https://github.com/${GITHUB_REPOSITORY}/releases/download/${TAG}/invctl_${VERSION}_linux_amd64
curl -LO https://github.com/${GITHUB_REPOSITORY}/releases/download/${TAG}/invctl_${VERSION}_checksums.txt

# Check it is the file this release published, before running it.
sha256sum -c invctl_${VERSION}_checksums.txt

chmod +x invctl_${VERSION}_linux_amd64
sudo install -m 0755 invctl_${VERSION}_linux_amd64 /usr/local/bin/invctl
invctl -version
```

Then follow [docs/INSTALL.md](https://github.com/${GITHUB_REPOSITORY}/blob/${TAG}/docs/INSTALL.md)
for the database, the systemd unit and TLS in front.

**Build it yourself instead?** `make build` needs outbound internet for rather
more than Go modules — it downloads the Tailwind standalone binary, around
110 MB, to compile the stylesheet. Build somewhere with network access and copy
the result to the target. That is the normal case, not the exception, and it is
why this release ships a binary at all.

## Upgrading

Read the **Action required** section above before deploying, then
[docs/UPGRADE.md](https://github.com/${GITHUB_REPOSITORY}/blob/${TAG}/docs/UPGRADE.md).

Migrations run at startup. `invctl -migrate` applies them and exits, so the
schema change and the restart can be separate steps — useful when you want to
take a backup between the two. There is no rollback command; `docs/UPGRADE.md`
explains why and what to do instead.

## Verifying this build

The binary is reproducible. `SOURCE_DATE_EPOCH` comes from the tagged commit's
own author date rather than the time the workflow ran, so building this tag
again next year produces the same bytes:

```bash
git clone https://github.com/${GITHUB_REPOSITORY} && cd invctl
git checkout ${TAG}
SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" make build
sha256sum bin/invctl    # matches invctl_${VERSION}_checksums.txt above
```

---

Full history in [CHANGELOG.md](https://github.com/${GITHUB_REPOSITORY}/blob/${TAG}/CHANGELOG.md).
Licensed [AGPL-3.0-only](https://github.com/${GITHUB_REPOSITORY}/blob/${TAG}/LICENSE).
