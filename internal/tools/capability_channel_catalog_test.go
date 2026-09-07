package tools

import "testing"

func TestChannelCatalogMetadataIsReadOnly(t *testing.T) {
	t.Parallel()
	if !inferMetadata("channel_catalog").IsReadOnly() {
		t.Fatal("channel_catalog metadata must be read-only")
	}
}
