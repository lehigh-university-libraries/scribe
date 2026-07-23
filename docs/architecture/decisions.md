# Architecture decisions

This page summarizes the decisions most often needed while navigating the
code. The complete authoritative set, including persistence, editor, security,
operations, and developer-experience invariants, is the
[engineering contract](../reference/engineering-contract.md).

## Canonical correction state

IIIF Presentation 3 AnnotationPage JSON is canonical. hOCR and other exports are
derived representations.

## Page identity and tenancy

The primary identity is workspace plus item image. Imported Canvas IDs remain
targets/provenance and can repeat across workspaces.

## Concurrency

Clients save complete pages with an expected revision. Conflicts are explicit
and require rebase or user resolution; last-write-wins is not accepted.

## Extensibility

Providers and segmentors implement registered interfaces and publish capability
descriptors. UI choices and defaults come from the registry rather than copied
switch statements.

## Deployment trust

Pull-request validation and image builds receive no cloud credentials. A
same-repository PR automatically requests a preview, but deployment waits for
a required reviewer only when repository operators have configured that rule
on the protected `preview` environment. The workflow binds the job to the
environment but cannot create its protection settings. Terraform and
credentialed helpers execute from the trusted base SHA, images are promoted by
digest, and the approved PR image runs only with preview-scoped identities.
