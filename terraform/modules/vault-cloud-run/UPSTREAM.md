# Vendored Vault module

This directory contains the runtime files from
[`libops/terraform-vault-cloudrun`](https://github.com/libops/terraform-vault-cloudrun)
release `0.5.3`, commit `bf62fe8cb4e8d391a357431894bf109d797d13a4`.

It is vendored because Terraform 1.15 performs shallow Git module fetches and
the upstream release's transitive `?ref=0.5.2` source cannot be resolved under
that behavior. The only intentional source change is to fetch
`libops/terraform-cloudrun-v2` from the immutable `0.5.2` commit archive.

Keep `LICENSE` with this module. When upgrading, replace the upstream files,
re-apply the immutable archive source, and run `make terraform-check`.
