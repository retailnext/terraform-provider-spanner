# Manage a database role. Roles are named containers for privileges and
# role membership - see the spanner_grant resource for privileges.
resource "spanner_role" "app_reader" {
  name = "app_reader"
}
