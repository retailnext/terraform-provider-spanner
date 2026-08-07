---
page_title: "Authentication - Spanner ACL Provider"
description: |-
  Configure authentication for the Spanner ACL provider using Google Application Default Credentials or explicit service account credentials.
---

# Authentication

The Spanner ACL provider talks to Google Cloud Spanner using the standard GCP client library
authentication scheme — the same one the official
[`hashicorp/google`](https://registry.terraform.io/providers/hashicorp/google/latest/docs) provider
uses. That means **the default authentication method just works**: none of the provider's
credential attributes are required, and in most environments (a workstation with `gcloud` logged
in, a GCE/GKE workload, Cloud Build, etc.) no provider-level credentials configuration is needed
at all.

## Application Default Credentials (default)

If no Terraform-specific credentials are specified, the provider will fall back to using Google
Application Default Credentials. To use them, you can enter the path of your service account key
file in the `GOOGLE_APPLICATION_CREDENTIALS` environment variable, or configure authentication
through one of the following;

- If you're running Terraform from a GCE instance, default credentials are automatically
  available. See [Creating and Enabling Service Accounts for
  Instances](https://cloud.google.com/compute/docs/access/create-enable-service-accounts-for-instances)
  for more details.

- On your workstation, you can make your Google identity available by running [`gcloud auth
  application-default login`](https://cloud.google.com/sdk/gcloud/reference/auth/application-default/login).

(Text above adapted from the `hashicorp/google` provider's [Provider Configuration
Reference](https://registry.terraform.io/providers/hashicorp/google/latest/docs/guides/provider_reference#credentials-1),
since Application Default Credentials resolution is identical across GCP-authenticating
providers.)

With Application Default Credentials configured, the provider block only needs to identify which
database to manage:

```terraform
provider "spanner" {
  project  = "my-gcp-project"
  instance = "my-spanner-instance"
  database = "my-database"
}
```

## Explicit Service Account Credentials

To authenticate with a specific service account key rather than whatever Application Default
Credentials resolve to — for example, to scope Terraform to a narrower identity than your own
user credentials — set `credentials` (inline JSON) or `credentials_file` (a path to the key
file). These two attributes are mutually exclusive.

```terraform
provider "spanner" {
  project          = "my-gcp-project"
  instance         = "my-spanner-instance"
  database         = "my-database"
  credentials_file = "/etc/gcp/spanner-sa.json"
}
```

```terraform
provider "spanner" {
  project     = "my-gcp-project"
  instance    = "my-spanner-instance"
  database    = "my-database"
  credentials = file("/etc/gcp/spanner-sa.json")
}
```

## Environment Variables

Sensitive or environment-specific values can be kept out of Terraform configuration files using
environment variables. The provider reads these before applying provider attributes:

| Variable                        | Provider Attribute | Description                                          |
| -------------------------------- | ------------------- | ----------------------------------------------------- |
| `SPANNER_PROJECT`                 | `project`            | GCP project ID containing the target Spanner instance |
| `SPANNER_INSTANCE`                | `instance`           | Spanner instance ID                                   |
| `SPANNER_DATABASE`                | `database`           | Spanner database ID to manage ACL objects on          |
| `SPANNER_CREDENTIALS`             | `credentials`        | Inline service account JSON key content               |
| `GOOGLE_APPLICATION_CREDENTIALS`  | —                    | Path to a service account key file, used by Application Default Credentials when no provider-level credentials are set |

With environment variables set, the provider block can be minimal or even empty:

```shell
export SPANNER_PROJECT=my-gcp-project
export SPANNER_INSTANCE=my-spanner-instance
export SPANNER_DATABASE=my-database
```

```terraform
provider "spanner" {}
```

## Required IAM Permissions

Whatever identity the provider authenticates as — a service account behind `credentials`/
`credentials_file`, or whichever identity Application Default Credentials resolves to — must be
granted the following roles on the target Spanner **instance or database** (grant at the
narrowest scope, a single database, rather than at the instance or project level where possible):

- [`roles/spanner.viewer`](https://cloud.google.com/iam/docs/roles-permissions/spanner)
- [`roles/spanner.databaseUser`](https://cloud.google.com/iam/docs/roles-permissions/spanner)

`roles/spanner.databaseUser` covers issuing the DDL (`CREATE ROLE`/`DROP ROLE`, and eventually
`GRANT`/`REVOKE`) this provider needs, as well as running the `INFORMATION_SCHEMA` queries used to
detect drift. No broader role (such as `roles/spanner.admin`) is required.
