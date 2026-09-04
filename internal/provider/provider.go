// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
	"google.golang.org/api/option"
)

// Ensure SpannerProvider satisfies various provider interfaces.
var _ provider.Provider = &spannerProvider{}

// spannerProvider defines the provider implementation.
type spannerProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

// spannerProviderModel describes the provider data model.
type spannerProviderModel struct {
	Project         types.String `tfsdk:"project"`
	Instance        types.String `tfsdk:"instance"`
	Database        types.String `tfsdk:"database"`
	Credentials     types.String `tfsdk:"credentials"`
	CredentialsFile types.String `tfsdk:"credentials_file"`
}

func (p *spannerProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "spanner"
	resp.Version = p.version
}

func (p *spannerProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Configure fine-grained access control (FGAC) for a single Google Cloud Spanner database",
		Attributes: map[string]schema.Attribute{
			"project": schema.StringAttribute{
				MarkdownDescription: "The GCP project ID containing the target Spanner instance. Can also be set via the `SPANNER_PROJECT` environment variable.",
				Optional:            true,
			},
			"instance": schema.StringAttribute{
				MarkdownDescription: "The Spanner instance ID. Can also be set via the `SPANNER_INSTANCE` environment variable.",
				Optional:            true,
			},
			"database": schema.StringAttribute{
				MarkdownDescription: "The Spanner database ID to manage ACL objects on. Can also be set via the `SPANNER_DATABASE` environment variable.",
				Optional:            true,
			},
			"credentials": schema.StringAttribute{
				MarkdownDescription: "Inline JSON service account key content to authenticate with, as an alternative to Application Default " +
					"Credentials. Mutually exclusive with `credentials_file`. Can also be set via the `SPANNER_CREDENTIALS` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"credentials_file": schema.StringAttribute{
				MarkdownDescription: "Path to a JSON service account key file to authenticate with. Mutually exclusive with `credentials`.",
				Optional:            true,
			},
		},
	}
}

func (p *spannerProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring Spanner client")

	// Retrieve provider data from configuration.
	var data spannerProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Default values to environment variables, but override with the
	// Terraform configuration value if set.

	project := os.Getenv("SPANNER_PROJECT")
	if !data.Project.IsNull() {
		project = data.Project.ValueString()
	}

	instance := os.Getenv("SPANNER_INSTANCE")
	if !data.Instance.IsNull() {
		instance = data.Instance.ValueString()
	}

	database := os.Getenv("SPANNER_DATABASE")
	if !data.Database.IsNull() {
		database = data.Database.ValueString()
	}

	// If any of the expected configurations are missing, return errors with
	// provider-specific guidance.

	if project == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("project"),
			"Missing Spanner Project",
			"The provider cannot create the Spanner client as there is a missing or empty value for the GCP project. "+
				"Set the project value in the configuration or use the SPANNER_PROJECT environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if instance == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("instance"),
			"Missing Spanner Instance",
			"The provider cannot create the Spanner client as there is a missing or empty value for the Spanner instance. "+
				"Set the instance value in the configuration or use the SPANNER_INSTANCE environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if database == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("database"),
			"Missing Spanner Database",
			"The provider cannot create the Spanner client as there is a missing or empty value for the Spanner database. "+
				"Set the database value in the configuration or use the SPANNER_DATABASE environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// credentials (inline JSON) and credentials_file are mutually exclusive.
	if !data.Credentials.IsNull() && !data.CredentialsFile.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("credentials"),
			"Conflicting Credentials Configuration",
			"Only one of `credentials` or `credentials_file` may be set, not both.",
		)
		resp.Diagnostics.AddAttributeError(
			path.Root("credentials_file"),
			"Conflicting Credentials Configuration",
			"Only one of `credentials` or `credentials_file` may be set, not both.",
		)
		// fail fast: this is a configuration error the user must resolve,
		// there is no value in proceeding with further configuration attempts.
		return
	}

	var opts []option.ClientOption

	credentialsJSON := os.Getenv("SPANNER_CREDENTIALS")
	if !data.Credentials.IsNull() {
		credentialsJSON = data.Credentials.ValueString()
	}

	switch {
	case credentialsJSON != "":
		tflog.Debug(ctx, "Configuring inline credentials for Spanner client")
		opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(credentialsJSON)))
	case !data.CredentialsFile.IsNull():
		tflog.Debug(ctx, "Configuring credentials file for Spanner client")
		opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, data.CredentialsFile.ValueString()))
	default:
		// Neither set: fall back to Application Default Credentials, which
		// the underlying Spanner client resolves on its own.
		tflog.Debug(ctx, "No explicit credentials configured; using Application Default Credentials")
	}

	databasePath := fmt.Sprintf("projects/%s/instances/%s/databases/%s", project, instance, database)
	ctx = tflog.SetField(ctx, "spanner_database_path", databasePath)
	tflog.Debug(ctx, "Creating spanner client")

	client, err := spanneracl.NewClient(ctx, databasePath, opts...)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Create Spanner Client",
			"An unexpected error was encountered trying to create the Spanner client. "+
				"Please verify the provider configuration values are correct and try again.\n\n"+
				err.Error(),
		)
		return
	}

	// Make the spanneracl client available during DataSource and Resource
	// type Configure methods.
	resp.DataSourceData = client
	resp.ResourceData = client

	tflog.Info(ctx, "Configured Spanner client", map[string]any{"success": true})
}

func (p *spannerProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewRoleResource,
		NewGrantResource,
	}
}

func (p *spannerProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		//NewExampleEphemeralResource,
	}
}

func (p *spannerProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		//NewExampleDataSource,
	}
}

func (p *spannerProvider) Actions(ctx context.Context) []func() action.Action {
	return []func() action.Action{
		//NewExampleAction,
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &spannerProvider{
			version: version,
		}
	}
}
