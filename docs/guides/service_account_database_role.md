---
page_title: "Service Accounts and Database Roles - Spanner ACL Provider"
description: |-
  Create a GCP service account, bind it to a Spanner database role with IAM, and connect as that role.
---

# Service Accounts and Database Roles

A database role created with `spanner_role` and given privileges with `spanner_table_grants` or
`spanner_grant` does nothing on its own: a principal can only act as a database role once IAM
allows it to. This guide shows the full chain in Terraform - the database role, its privileges, a
GCP service account, and the IAM bindings tying the service account to the role - and how a client
then connects as that role.

See [About fine-grained access
control](https://cloud.google.com/spanner/docs/fgac-about) and [Configure fine-grained access
control](https://cloud.google.com/spanner/docs/configure-fgac) for Spanner's own documentation.

## Required IAM Roles

A principal that uses a database role needs two IAM roles on the database:

- [`roles/spanner.fineGrainedAccessUser`](https://cloud.google.com/iam/docs/roles-permissions/spanner)
  makes the principal a fine-grained access control user.
- [`roles/spanner.databaseRoleUser`](https://cloud.google.com/iam/docs/roles-permissions/spanner),
  with an IAM condition naming the database role, lets the principal act as that specific role.
  Without the condition, the principal can act as *every* database role in the database. To allow
  several roles in one binding, combine them in the condition:
  `resource.type == "spanner.googleapis.com/DatabaseRole" && (resource.name.endsWith("/databaseRoles/role1") || resource.name.endsWith("/databaseRoles/role2"))`.

Don't also give the principal a database-level role such as `roles/spanner.databaseUser` or
`roles/spanner.databaseReader`. Those grant access to the whole database without a database role,
and Google recommends revoking them so that a principal's access is governed by only one method.
The exception is `roles/spanner.viewer`, which every user of the Google Cloud console needs.

## Example

The `google_*` resources come from the
[`hashicorp/google`](https://registry.terraform.io/providers/hashicorp/google/latest/docs)
provider.

```terraform
locals {
  project  = "my-gcp-project"
  instance = "my-spanner-instance"
  database = "my-database"
}

provider "spanner" {
  project  = local.project
  instance = local.instance
  database = local.database
}

# The database role and its privileges.
resource "spanner_role" "reader" {
  name = "reader"
}

resource "spanner_table_grants" "orders" {
  table = "orders"

  grant {
    role       = spanner_role.reader.name
    privileges = ["SELECT"]
  }
}

# The service account that will act as the reader database role.
resource "google_service_account" "reader" {
  project    = local.project
  account_id = "spanner-reader"
}

resource "google_spanner_database_iam_member" "reader_fgac_user" {
  project  = local.project
  instance = local.instance
  database = local.database
  role     = "roles/spanner.fineGrainedAccessUser"
  member   = "serviceAccount:${google_service_account.reader.email}"
}

resource "google_spanner_database_iam_member" "reader_role_user" {
  project  = local.project
  instance = local.instance
  database = local.database
  role     = "roles/spanner.databaseRoleUser"
  member   = "serviceAccount:${google_service_account.reader.email}"

  condition {
    title      = "Binding to ${spanner_role.reader.name} role"
    expression = "resource.type == \"spanner.googleapis.com/DatabaseRole\" && resource.name.endsWith(\"/databaseRoles/${spanner_role.reader.name}\")"
  }
}
```

The condition references `spanner_role.reader.name` rather than repeating the literal, so the
binding is created after the database role and follows it if it's renamed.

-> **Note:** If the Spanner instance and database are created in the same configuration, configure
the provider from their `name` attributes set in config (e.g. `database =
google_spanner_database.example.name`), which are known at plan time. The provider can't be
configured from values that are only known after apply. Referencing the resources also makes every
`spanner_*` resource wait until the database exists.

## Connecting as the Database Role

IAM only *allows* the service account to use the database role. A fine-grained access control
user must also specify the database role to run queries, DML or row operations, so every client
must name the role when it connects. The database role's privileges are what it can access: a
fine-grained access control user also gets the system `public` role, which has no privileges by
default.

### Go client

Set `DatabaseRole` in `spanner.ClientConfig`. It applies to every session the client creates, so a
client acts as exactly one database role - use a separate client for each role.

```go
client, err := spanner.NewClientWithConfig(ctx,
	"projects/my-gcp-project/instances/my-spanner-instance/databases/my-database",
	spanner.ClientConfig{DatabaseRole: "reader"},
)
```

### gcloud

Pass `--database-role`. To test as the service account from a workstation, impersonate it - this
needs `roles/iam.serviceAccountTokenCreator` on the service account (project `roles/owner` does not
include it):

```shell
gcloud spanner databases execute-sql my-database \
  --project=my-gcp-project --instance=my-spanner-instance \
  --database-role=reader \
  --impersonate-service-account=spanner-reader@my-gcp-project.iam.gserviceaccount.com \
  --sql="SELECT * FROM orders"
```

## Don't Mix `spanner_table_grants` and `spanner_grant` on One Table

Use either `spanner_table_grants` or `spanner_grant` resources for a given table, never both.
`spanner_table_grants` owns every grant on its table and revokes any grant not in its `grant`
blocks - including grants made by `spanner_grant` resources on the same table, and all column-level
grants. The two resources race: within one apply, whichever runs last decides which grants remain,
and Terraform doesn't order them, so the outcome can differ from run to run. Afterwards they keep
undoing each other, so every plan shows changes again.

It's fine to use both resource types in one configuration as long as each table is managed by only
one of them - for example `spanner_table_grants` on one table, and column-level or view grants with
`spanner_grant` on others.
