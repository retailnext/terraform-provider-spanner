# Manage a database role. Roles are named containers for privileges and
# role membership - see the (planned) spanner_grant resource for those.
resource "spanner_role" "app_reader" {
  name = "app_reader"
}
