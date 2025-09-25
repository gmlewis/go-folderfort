package folderfort

import (
	"errors"
	"net/http"
)

func NewClientWithAPIToken(server, apiToken string) (*Client, error) {
	if server == "" || apiToken == "" {
		return nil, errors.New("missing server or apiToken")
	}

	authOpt := func(c *Client) error {
		c.Client = &doerWithToken{apiToken: apiToken}
		return nil
	}

	return NewClient(server, authOpt)
}

type doerWithToken struct {
	apiToken string
}

var _ HttpRequestDoer = &doerWithToken{}

func (d *doerWithToken) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+d.apiToken)
	client := &http.Client{}
	return client.Do(req)
}
