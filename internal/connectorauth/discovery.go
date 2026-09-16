package connectorauth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type metadataTransport struct {
	base   http.RoundTripper
	status int
}

func (t *metadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !secureURL(req.URL.String()) {
		return nil, fmt.Errorf("insecure metadata destination")
	}
	response, err := t.base.RoundTrip(req)
	if err == nil {
		t.status = response.StatusCode
	}
	return response, err
}

func (m *Manager) resourceMetadata(ctx context.Context, metadata, resource string) (*oauthex.ProtectedResourceMetadata, bool, error) {
	client := *m.client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &metadataTransport{base: base}
	client.Transport = transport
	prm, err := oauthex.GetProtectedResourceMetadata(ctx, metadata, resource, &client)
	missing := transport.status == http.StatusNotFound || transport.status == http.StatusGone || transport.status == http.StatusMethodNotAllowed
	return prm, missing, err
}
