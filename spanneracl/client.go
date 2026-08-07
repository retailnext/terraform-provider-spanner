// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"fmt"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"google.golang.org/api/option"
)

// Client wraps the Google Cloud Spanner clients needed to manage
// fine-grained access control objects (roles, grants) on a single
// Spanner database.
type Client struct {
	AdminClient  *database.DatabaseAdminClient
	DataClient   *spanner.Client
	DatabasePath string // projects/{project}/instances/{instance}/databases/{database}
}

// NewClient creates a Client for the given database, e.g.
// "projects/my-project/instances/my-instance/databases/my-database".
func NewClient(ctx context.Context, databasePath string, opts ...option.ClientOption) (*Client, error) {
	adminClient, err := database.NewDatabaseAdminClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create spanner database admin client: %w", err)
	}

	dataClient, err := spanner.NewClient(ctx, databasePath, opts...)
	if err != nil {
		_ = adminClient.Close()
		return nil, fmt.Errorf("failed to create spanner data client: %w", err)
	}

	return &Client{
		AdminClient:  adminClient,
		DataClient:   dataClient,
		DatabasePath: databasePath,
	}, nil
}

// Close releases resources held by the underlying Spanner clients.
func (c *Client) Close() {
	c.DataClient.Close()
	_ = c.AdminClient.Close()
}
