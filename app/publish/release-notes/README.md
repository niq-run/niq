# Release notes

Two things live here:

- **`release-notes/vX.Y.Z.md`** — one file per release, named after its tag.
  This is the archive and the primary source the release workflow reads.
- **`RELEASE_NOTES.md`** — the notes for the *current* release, kept at a stable
  path. The workflow uses it as a fallback when the tag's own archive file is
  missing (and only if it mentions the tag being released, so a stale copy can
  never publish a previous version's notes under a new tag).

## Cutting a release

1. Write/refresh `app/publish/RELEASE_NOTES.md` for the version you are about
   to tag.
2. Archive it: `cp app/publish/RELEASE_NOTES.md app/publish/release-notes/vX.Y.Z.md`.
3. Commit, then tag and push: `git tag -a vX.Y.Z -m ... && git push origin vX.Y.Z`.

The release workflow (`.github/workflows/release.yml`) then:

- uses `app/publish/release-notes/${TAG}.md` when it exists;
- otherwise falls back to `app/publish/RELEASE_NOTES.md` **only if that file
  mentions the tag**;
- otherwise lets goreleaser generate a changelog from the commits since the
  previous tag.

To fix the body of an already published release, edit it in place:

```sh
gh release edit vX.Y.Z --notes-file app/publish/release-notes/vX.Y.Z.md
```
