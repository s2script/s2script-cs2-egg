// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Detail struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	AppID    int    `json:"app_id"`
	Size     int64  `json:"size"`
	Updated  int64  `json:"updated"`
}

func number(s string) bool {
	if s == "" || len(s) > 20 || s[0] == '0' {
		return false
	}
	_, e := strconv.ParseUint(s, 10, 64)
	return e == nil
}
func (d *Document) Revision(id string) (string, int64, error) {
	app := d.Root.Child("AppWorkshop")
	a := app.Child("WorkshopItemsInstalled").Child(id)
	b := app.Child("WorkshopItemDetails").Child(id)
	v := a.Value("manifest")
	if !number(v) {
		return "", 0, errors.New("legacy/unversioned item stays local")
	}
	if b.Value("manifest") != v || b.Value("latest_manifest") != "" && b.Value("latest_manifest") != v {
		return "", 0, errors.New("incomplete/pending workshop revision")
	}
	size, e := strconv.ParseInt(a.Value("size"), 10, 64)
	if e != nil || size < 0 {
		return "", 0, errors.New("invalid ACF size")
	}
	return v, size, nil
}
func SteamDetails(ctx context.Context, ids []string) (map[string]Detail, error) {
	return details(ctx, &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Steam redirect refused") }}, "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/", ids)
}
func details(ctx context.Context, c *http.Client, endpoint string, ids []string) (map[string]Detail, error) {
	if len(ids) > 100 {
		return nil, errors.New("too many workshop items")
	}
	form := url.Values{"itemcount": {strconv.Itoa(len(ids))}}
	for i, id := range ids {
		if !number(id) {
			return nil, errors.New("invalid item")
		}
		form.Set("publishedfileids["+strconv.Itoa(i)+"]", id)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := c.Do(req)
	if e != nil {
		return nil, errors.New("Steam metadata unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("Steam metadata unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil || len(b) > 4<<20 {
		return nil, errors.New("Steam metadata exceeds bound")
	}
	var v struct {
		Response struct {
			Details []struct {
				ID       string          `json:"publishedfileid"`
				Revision string          `json:"hcontent_file"`
				Result   int             `json:"result"`
				AppID    int             `json:"consumer_app_id"`
				Size     json.RawMessage `json:"file_size"`
				Updated  int64           `json:"time_updated"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return nil, e
	}
	out := map[string]Detail{}
	for _, d := range v.Response.Details {
		if _, ok := out[d.ID]; ok {
			return nil, errors.New("duplicate Steam item")
		}
		if d.Result != 1 || d.AppID != 730 || !number(d.ID) || !number(d.Revision) {
			return nil, errors.New("Steam item identity unavailable")
		}
		size, e := strconv.ParseInt(strings.Trim(string(d.Size), "\""), 10, 64)
		if e != nil || size < 0 {
			return nil, errors.New("invalid Steam item size")
		}
		out[d.ID] = Detail{d.ID, d.Revision, d.AppID, size, d.Updated}
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, errors.New("Steam omitted item")
		}
	}
	return out, nil
}
