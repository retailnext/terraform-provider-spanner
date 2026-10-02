# Authoritatively manage every grant on the "orders" table: app_reader gets
# SELECT, app_writer gets SELECT/INSERT/UPDATE, and any other grant on the
# table - including column-level grants and grants made outside Terraform -
# is revoked.
resource "spanner_table_grants" "orders" {
  table = "orders"

  grant {
    role       = spanner_role.app_reader.name
    privileges = ["SELECT"]
  }

  grant {
    role       = spanner_role.app_writer.name
    privileges = ["SELECT", "INSERT", "UPDATE"]
  }
}

# A table in a named schema.
resource "spanner_table_grants" "sales_invoices" {
  schema = "sales"
  table  = "invoices"

  grant {
    role       = spanner_role.app_reader.name
    privileges = ["SELECT"]
  }
}
