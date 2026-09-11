# Import a grant resource by its synthetic ID:
# "role_name|privilege|resource_type|resource|columns" (columns
# comma-separated, empty for a table-level grant).
terraform import spanner_grant.app_reader_select 'app_reader|SELECT|TABLE|orders|'
terraform import spanner_grant.app_reader_select_customer_columns 'app_reader|SELECT|TABLE|customers|id,email'
