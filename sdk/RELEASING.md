# Releasing the SDKs

All eight SDKs are released together at one version, so a version number means the same features in every language.

1. Set `info.version` in `spec/openapi.json`, run `go run .` in `scripts/gen` (it writes the version into every
   SDK and manifest), and merge that to main.
2. From an up-to-date main, run `sdk/scripts/release.sh <version>`. It checks that the tree, the spec and the
   generated code are at that version, then pushes one tag per SDK: `sdk/<lang>/v<version>`, e.g.
   `sdk/python/v0.2.0`. Go needs its own tag prefix, which is why there are eight tags and not one.

`.github/workflows/release-sdk.yml` runs once per tag: it checks that the tag matches the version in the manifest,
runs the tests and publishes.

The workflow runs in the GitHub environment `release` (add required reviewers there to gate every publish).

## One-time setup per registry

Trusted publishing means no long-lived token is stored anywhere: the registry trusts this repository's workflow.
For every registry below the trusted publisher is: owner `Recodive`, repository `hansho`, workflow
`release-sdk.yml`, environment `release`.

| SDK | Registry | Name | Setup |
|---|---|---|---|
| Node | npm | `@recodive/hansho` | Create the `recodive` npm org. npm trusted publishing is configured on an existing package, so the first publish is `npm publish --access public` by hand (from `sdk/node`, after `npm run build`); then package settings > Trusted publisher > GitHub Actions |
| Python | PyPI | `hansho` | pypi.org > Your account > Publishing > add a *pending* publisher for `hansho` (works before the first upload) |
| Ruby | RubyGems | `hansho` | rubygems.org > Settings > Trusted publishers > *pending* trusted publisher for `hansho` |
| .NET | NuGet | `Hansho` | nuget.org > Trusted publishing > add a policy; set the repository variable `NUGET_USER` to the nuget.org account name |
| Rust | crates.io | `hansho` | The first release needs the secret `CARGO_REGISTRY_TOKEN` (scope publish-new); then crate settings > Trusted publishing, and delete the secret |
| Java | Maven Central | `app.hansho:hansho` | Verify the namespace `app.hansho` at central.sonatype.com (a DNS TXT record on hansho.app), then the secrets `MAVEN_CENTRAL_USERNAME` / `MAVEN_CENTRAL_PASSWORD` (a Central user token) and `MAVEN_GPG_PRIVATE_KEY` / `MAVEN_GPG_PASSPHRASE` (a signing key published to keys.openpgp.org) |
| Go | proxy.golang.org | `github.com/recodive/hansho/sdk/go` | Nothing to set up: the tag `sdk/go/v0.1.0` is the release (pkg.go.dev picks it up on first `go get`) |
| PHP | Packagist | `recodive/hansho` | Not automated yet: Packagist reads `composer.json` at a repository's root, so the PHP SDK needs a read-only split repository (`Recodive/hansho-php`) mirrored from `sdk/php` |
