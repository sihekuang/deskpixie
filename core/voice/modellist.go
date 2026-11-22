package voice

// What the adapters' ModelCatalog share (#477): the order of a list and one GET of the
// provider's model list, with short errors that never hold the key.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ListTimeout bounds one model-list fetch (every page of it).
const ListTimeout = 10 * time.Second

const (
	listBodyLimit = 8 << 20
	listMsgLimit  = 200
)

// OrderModels is ids without repeats or blanks: def first when it is among them, then the
// rest by name.
func OrderModels(ids []string, def string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		switch {
		case a == def:
			return -1
		case b == def:
			return 1
		}
		return strings.Compare(a, b)
	})
	return out
}

// GetJSON GETs u with header and decodes the reply into out. Errors start with what
// ("openai models") and say only the HTTP status and the API's message, or why the request
// failed: never the URL, and any header value (the key) is blanked out.
func GetJSON(ctx context.Context, c *http.Client, what, u string, header http.Header, out any) error {
	fail := func(msg string) error { return fmt.Errorf("%s: %s", what, blankHeaders(msg, header)) }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fail("bad url")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return fail("timed out")
		case errors.As(err, &ue):
			return fail(ue.Err.Error())
		}
		return fail(err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, listBodyLimit))
	if err != nil {
		return fail("reply cut off")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if m := strings.TrimSpace(e.Error.Message); m != "" {
			return fail(fmt.Sprintf("http %d: %s", resp.StatusCode, clipTo(m, listMsgLimit)))
		}
		return fail(fmt.Sprintf("http %d", resp.StatusCode))
	}
	if json.Unmarshal(body, out) != nil {
		return fail("unreadable reply")
	}
	return nil
}

// blankHeaders is msg with every header value, and the token of an "Authorization: Bearer"
// one, replaced by ***.
func blankHeaders(msg string, header http.Header) string {
	for _, vs := range header {
		for _, v := range vs {
			for _, s := range []string{v, strings.TrimPrefix(v, "Bearer ")} {
				if s != "" {
					msg = strings.ReplaceAll(msg, s, "***")
				}
			}
		}
	}
	return msg
}

func clipTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
