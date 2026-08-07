# Authenticate with Application Default Credentials (recommended)
provider "spanner" {
  project  = "my-gcp-project"
  instance = "my-spanner-instance"
  database = "my-database"
}
