// Copyright RetailNext, Inc. 2026

package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccProtoV6ProviderFactories is used to instantiate a provider during
// acceptance/unit testing. The factory function is called for each
// Terraform CLI command to create a provider server that the CLI can
// connect to and interact with.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"spanner": providerserver.NewProtocol6WithError(New("test")()),
}

// A resource block is required in each config below so Terraform actually
// initializes and configures the provider - a `provider` block with no
// resource/data source referencing it is never configured. Configure fails
// with a diagnostic before any CRUD is attempted, so these are plain unit
// tests: no real Spanner instance is touched.

func TestAccProviderConfigMissingProject(t *testing.T) {
	t.Setenv("SPANNER_PROJECT", "")
	t.Setenv("SPANNER_INSTANCE", "")
	t.Setenv("SPANNER_DATABASE", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  instance = "test-instance"
  database = "test-db"
}
resource "spanner_role" "example" {
  name = "example"
}
`,
				ExpectError: regexp.MustCompile(`Missing Spanner Project`),
			},
		},
	})
}

func TestAccProviderConfigMissingInstance(t *testing.T) {
	t.Setenv("SPANNER_PROJECT", "")
	t.Setenv("SPANNER_INSTANCE", "")
	t.Setenv("SPANNER_DATABASE", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project  = "test-project"
  database = "test-db"
}
resource "spanner_role" "example" {
  name = "example"
}
`,
				ExpectError: regexp.MustCompile(`Missing Spanner Instance`),
			},
		},
	})
}

func TestAccProviderConfigMissingDatabase(t *testing.T) {
	t.Setenv("SPANNER_PROJECT", "")
	t.Setenv("SPANNER_INSTANCE", "")
	t.Setenv("SPANNER_DATABASE", "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project  = "test-project"
  instance = "test-instance"
}
resource "spanner_role" "example" {
  name = "example"
}
`,
				ExpectError: regexp.MustCompile(`Missing Spanner Database`),
			},
		},
	})
}

func TestAccProviderConfigCredentialsConflict(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project          = "test-project"
  instance         = "test-instance"
  database         = "test-db"
  credentials      = "{}"
  credentials_file = "/tmp/does-not-matter.json"
}
resource "spanner_role" "example" {
  name = "example"
}
`,
				ExpectError: regexp.MustCompile(`Conflicting Credentials Configuration`),
			},
		},
	})
}
