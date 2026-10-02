# Import the grants on a table by its identifier: "table", or "schema.table"
# for a table in a named schema. The table must already have at least one grant.
terraform import spanner_table_grants.orders orders
terraform import spanner_table_grants.sales_invoices sales.invoices
