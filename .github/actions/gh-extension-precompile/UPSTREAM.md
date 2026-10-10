# Upstream

Vendored from [cli/gh-extension-precompile v2.1.0](https://github.com/cli/gh-extension-precompile/tree/9e2237c30f869ad3bcaed6a4be2cd43564dd421b), commit `9e2237c30f869ad3bcaed6a4be2cd43564dd421b`, under its included MIT license.

The only local changes are immutable action references in `action.yml`:

- `actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff` (recorded `v5`).
- `actions/attest-build-provenance@ef244123eb79f2f7a7e75d99086184180e6d0018` (recorded `v1`).

These commits come from this repository's existing workflow lock. The attestation action already pins its Node action children to `actions/attest-build-provenance/predicate@36fa7d009e22618ca7cd599486979b8150596c74` and `actions/attest@67422f5511b7ff725f4dbd6fb9bd2cd925c65a8d`; neither has further action dependencies.

The upstream build script is unchanged, including its twelve default platform binaries, optional Android builds, release flags, signing, and build override inputs. Vendoring its composite manifest freezes the complete release action graph even when GitHub ignores a malformed workflow lock.
