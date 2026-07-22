# Permissions

Authentication establishes a principal. Route-level Connect and HTTP
middleware resolves workspace/resource grants before a handler runs. Handlers
focus on product validation and persistence and do not grow one-off ownership
rules.

The workspace roles are:

- `admin`: membership and workspace administration
- `write`: edit workspace resources
- `create`: create new items
- `read`: view resources without mutation

Authorization checks are side-effect free. Importing a manifest, resolving a
remote resource, starting provider work, or creating an index is an explicit
operation with the corresponding grant.

API keys are workspace-scoped credentials and are always constrained by their
configured workspace role and scopes, even when a system administrator created
them; the creator's privileges are never inherited by the key. OAuth sessions
are user-scoped. Each request selects a workspace and becomes workspace-scoped
only after current membership and role are revalidated. Never forward browser
credentials to a separate IIIF or provider origin. Internal Cloud Run requests
use an exact-origin, exact-audience service identity.

Credential and membership catalogs have database-serialized admission limits,
so concurrent API replicas cannot overfill them: a user may access at most 50
workspaces, a workspace may contain at most 100 members, 100 API keys, and 100
provider-secret locators. Lists have the same fixed upper bounds. Each user may
hold at most 20 OAuth sessions; creating another session evicts the oldest and
the worker deletes expired sessions in bounded batches. These are greenfield
product invariants, not pagination substitutes or compatibility behavior.
