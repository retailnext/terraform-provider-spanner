// Copyright RetailNext, Inc. 2026

package testutil

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcspanner "github.com/testcontainers/testcontainers-go/modules/gcloud/spanner"
)

// SpannerEmulatorImage pins the Cloud Spanner emulator image used by tests.
const SpannerEmulatorImage = "gcr.io/cloud-spanner-emulator/emulator:1.5.56"

// NewTestSpannerContainer starts a Cloud Spanner emulator container and
// returns its URI and project ID. The container is automatically
// terminated when the test finishes.
func NewTestSpannerContainer(t *testing.T) (uri string, projectID string) {
	ctx := context.Background()

	container, err := tcspanner.Run(ctx, SpannerEmulatorImage)
	if err != nil {
		t.Fatalf("failed to start the spanner emulator container: %s", err)
	}

	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Fatalf("failed to terminate the spanner emulator container: %s", err)
		}
	})

	return container.URI(), container.ProjectID()
}
