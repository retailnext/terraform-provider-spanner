# Terraform Provider for Spanner ACL

This provider plugin allows configuring fine-grained access control (FGAC) for Google Cloud
Spanner through Terraform, including database roles, privilege grants, and role membership. It
is modeled after the sibling project,
[`terraform-provider-scylladb`](https://github.com/retailnext/terraform-provider-scylladb), which
manages the same kind of ACL concepts for ScyllaDB.

**Status: early development.** The `spanner_role` resource is implemented and unit-tested; grants
and role membership are not yet built.

It contains, or will contain, the following:

- `spanneracl/`: client for Cloud Spanner and abstracted methods to manage ACL objects (roles, grants)
- `internal/provider/`: Terraform provider and resources (`spanner_role`; more to come)
- `internal/testutil/`: shared test helpers, including a real Cloud Spanner emulator spun up via testcontainers
- `examples/`: example Terraform configurations, embedded into generated docs
- `templates/`: hand-written doc templates (`tfplugindocs` source of truth) — edit these, not `docs/`
- `docs/`: **generated** reference docs and guides — run `make generate` after editing `templates/`
  or `examples/`; do not edit files under `docs/` directly, they get overwritten
- `tools/`: pinned dev-tool versions (`tfplugindocs`, `copywrite`) for `make generate`, isolated
  into their own Go module so they don't pollute the provider's own `go.mod`

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.4
- [Go](https://golang.org/doc/install) >= 1.26
- [Docker](https://www.docker.com/) (required to run the test suite against the Cloud Spanner emulator)

### Required GCP IAM permissions

Whatever identity/credentials the provider authenticates as must be granted the following roles
on the target Spanner **instance or database** (grant at the narrowest scope — a single
database — rather than at the instance or project level where possible):

- `roles/spanner.viewer`
- `roles/spanner.databaseUser`

### Provider configuration

The provider manages ACL objects on a single Spanner database, configured via:

| Attribute          | Env var              | Required | Notes                                                          |
| ------------------ | --------------------- | -------- | --------------------------------------------------------------- |
| `project`           | `SPANNER_PROJECT`      | yes      | GCP project ID                                                  |
| `instance`          | `SPANNER_INSTANCE`     | yes      | Spanner instance ID                                             |
| `database`          | `SPANNER_DATABASE`     | yes      | Spanner database ID                                             |
| `credentials`       | `SPANNER_CREDENTIALS`  | no       | Inline service account JSON key. Mutually exclusive with below. |
| `credentials_file`  | —                      | no       | Path to a service account JSON key file.                        |

If neither `credentials` nor `credentials_file` is set, the provider falls back to
[Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) —
see the generated [authentication guide](docs/guides/authentication.md) (source:
[`templates/guides/authentication.md.tmpl`](templates/guides/authentication.md.tmpl)) for details.

```hcl
provider "spanner" {
  project  = "my-gcp-project"
  instance = "my-spanner-instance"
  database = "my-database"
}

resource "spanner_role" "app_reader" {
  name = "app_reader"
}
```

## Building The Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command:

```shell
go install
```

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) and
[Docker](https://www.docker.com/) installed on your machine (see [Requirements](#requirements)
above). Docker is required because the test suite exercises a real Cloud Spanner emulator
container instead of mocks.

```shell
go build ./...            # compile
go vet ./...               # static analysis
gofmt -l .                  # formatting check
go test -v -cover ./...     # unit tests + provider-config unit tests + emulator-backed client tests
```

*Note:* the emulator-backed tests in `spanneracl/` start real Docker containers via
[testcontainers-go](https://golang.testcontainers.org/), so they run slower than typical unit
tests.

⚠️ **Some tests can't run against the emulator.** The Cloud Spanner emulator doesn't implement
`INFORMATION_SCHEMA.ROLES`, `TABLE_PRIVILEGES` or `COLUMN_PRIVILEGES`, and rejects column-level
grants
([cloud-spanner-emulator#350](https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350)).
Tests that read roles or grants back - including every Terraform acceptance test, since each
`resource.Test` step runs a post-apply `Read` - run against a real Spanner database instead, and
are skipped unless one is configured. See
[Running tests against a real Spanner database](#running-tests-against-a-real-spanner-database).

### Running tests against a real Spanner database

The real-Spanner tests run only when `SPANNER_TEST_DATABASE` is set to the full path of an
existing GoogleSQL-dialect database; without it they're skipped, so the commands above never touch
a real database. The Terraform acceptance tests (`TestAcc*` using `resource.Test`) additionally
need `TF_ACC=1`.

1. Authenticate with Application Default Credentials. The identity needs the same roles as the
   provider itself (see [Required GCP IAM permissions](#required-gcp-iam-permissions)) on the test
   database - `roles/spanner.databaseUser` includes the DDL permission the tests use:

   ```shell
   gcloud auth application-default login
   ```

2. Point the tests at the database:

   ```shell
   export SPANNER_TEST_DATABASE=projects/<project>/instances/<instance>/databases/<database>
   ```

3. Run the tests. Docker is still needed for the emulator-backed tests, which run alongside:

   ```shell
   # Unit, emulator and real-Spanner client tests; Terraform acceptance tests are skipped
   go test -v -cover -timeout 45m ./...

   # Everything above plus the Terraform acceptance tests (sets TF_ACC=1)
   make testacc

   # A single test, e.g. the spanner_table_grants acceptance test
   TF_ACC=1 go test -v -run TestAccTableGrantsResource ./internal/provider
   ```

   Don't use `make test` with `SPANNER_TEST_DATABASE` set: its 120-second timeout is far shorter
   than a real-Spanner run. Every schema change on real Spanner takes seconds, so a full run takes
   around 20 minutes.

Each test creates its own uniquely named tables, views and roles (prefixed `acltest_`) and removes
them when it finishes, so it's safe to use a database other work shares. A run that's interrupted
(Ctrl-C, or a `go test` timeout) can skip that cleanup; to find leftovers:

```shell
gcloud spanner databases execute-sql <database> --project=<project> --instance=<instance> \
  --sql="SELECT TABLE_TYPE, TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_NAME LIKE 'acltest_%'
         UNION ALL SELECT 'ROLE', ROLE_NAME FROM INFORMATION_SCHEMA.ROLES WHERE ROLE_NAME LIKE 'acltest_%'"
```

To remove them, revoke any grants the `acltest_` roles still hold, then drop the roles, then any
views, then the tables (Spanner won't drop a role that holds privileges or a table a view depends
on).

To regenerate documentation after editing `templates/` or `examples/`, run:

```shell
make generate
```

This runs (via the isolated `tools/` Go module, so `tfplugindocs`/`copywrite` versions stay
pinned independently of the provider's own dependencies):

1. `copywrite headers` — adds/updates copyright headers (respects `.copywrite.hcl`'s
   `header_ignore`, which excludes `examples/**` and `test_cmds/**`)
2. `terraform fmt -recursive examples/` — formats the example `.tf` files
3. `tfplugindocs generate` — renders `templates/*.md.tmpl` (interpolating live schema via
   `{{ .SchemaMarkdown }}` and example files via `{{ tffile ... }}`/`{{ codefile ... }}`) into
   `docs/`

`docs/` is fully regenerated on each run — edit `templates/` (or `examples/`), not `docs/`
directly.

### Using the local provider

The following is general guidance on how to use the local provider you are developing in
Terraform code before it is published.

1. Using `dev_overrides` path

   Follow the official direction [here](https://developer.hashicorp.com/terraform/tutorials/providers-plugin-framework/providers-plugin-framework-provider-configure).
   This allows you to use the provider by running `go install .`. Your `~/.terraformrc` would look like:

   ```
   provider_installation {

   dev_overrides {
       "registry.terraform.io/retailnext/spanner" = "/Users/myusername/go/bin"
   }

   # For all other providers, install them directly from their origin provider
   # registries as normal. If you omit this, Terraform will _only_ use
   # the dev_overrides block, and so no other providers will be available.
   direct {}
   }
   ```

   Note that once `dev_overrides` is added, you cannot run `terraform init` if
   `registry.terraform.io/retailnext/spanner` appears in the Terraform code — see the
   "release" binary method below if you need other, non-local providers alongside it.

   If you are using `tofu`, update `~/.tofurc` and use `registry.opentofu.org` as the provider registry.

2. Using the local "release" binary

   By manually doing what `terraform init` would have done, you can use the local code. The
   following example shows the steps in a Linux environment with an amd64 processor.

   ```shell
   CGO_ENABLED=0 go build -trimpath -o terraform-provider-spanner_v1.0.0 -ldflags "-s -w -X main.version=1.0.0" .
   mkdir -p ~/.terraform.d/plugins/local.providers/local/spanner/1.0.0/linux_amd64
   mv terraform-provider-spanner_v1.0.0 ~/.terraform.d/plugins/local.providers/local/spanner/1.0.0/linux_amd64

   cat <<EOF > $HOME/.terraformrc
   provider_installation {
       filesystem_mirror {
           path = "/home/runner/.terraform.d/plugins"
           include = ["local.providers/*/*"]
       }
       direct {
           exclude = ["local.providers/*/*"]
       }
   }
   EOF
   ```

   If you are using `tofu`, use `$HOME/.tofurc` instead of `$HOME/.terraformrc` in the example.
