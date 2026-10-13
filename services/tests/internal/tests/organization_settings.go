package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type OrganizationTestSettings struct {
	OrganizationID      int64 `json:"organization_id"`
	RecalculateOnChange *bool `json:"recalculate_on_test_change"`
	Revision            int64 `json:"revision"`
}

func (d *Dependencies) OrganizationTestSettings(ctx context.Context, organization int64) (OrganizationTestSettings, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/internal/core/organizations/%d/test-settings", d.CoreURL, organization), nil)
	if e != nil {
		return OrganizationTestSettings{}, unavailable
	}
	req.Header.Set("X-Service-Key", d.Key)
	response, e := d.HTTP.Do(req)
	if e != nil {
		return OrganizationTestSettings{}, unavailable
	}
	defer response.Body.Close()
	var body struct {
		Status bool                     `json:"status"`
		Data   OrganizationTestSettings `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body) != nil || !body.Status || body.Data.OrganizationID != organization || body.Data.RecalculateOnChange == nil || body.Data.Revision < 0 {
		return OrganizationTestSettings{}, unavailable
	}
	return body.Data, nil
}
