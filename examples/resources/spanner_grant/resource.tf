# Grant a table-level privilege to a role.
resource "spanner_grant" "app_reader_select" {
  role_name     = spanner_role.app_reader.name
  privilege     = "SELECT"
  resource_type = "TABLE"
  resource      = "orders"
}

# Grant a column-level privilege by listing columns - only SELECT, INSERT,
# and UPDATE support column-level grants, and only on TABLE resources.
resource "spanner_grant" "app_reader_select_customer_columns" {
  role_name     = spanner_role.app_reader.name
  privilege     = "SELECT"
  resource_type = "TABLE"
  resource      = "customers"
  columns       = ["id", "email"]
}
