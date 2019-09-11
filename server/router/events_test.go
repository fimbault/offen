package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/offen/offen/server/persistence"
)

type mockInsertDatabase struct {
	persistence.Database
	err error
}

func (m *mockInsertDatabase) Insert(string, string, string) error {
	return m.err
}

func TestRouter_PostEvents(t *testing.T) {
	tests := []struct {
		name               string
		db                 persistence.Database
		body               io.Reader
		userID             interface{}
		expectedStatusCode int
		expectedResponse   string
	}{
		{
			"empty body",
			&mockInsertDatabase{},
			nil,
			"user-identifier",
			http.StatusBadRequest,
			`{"error":"EOF","status":400}`,
		},
		{
			"malformed body",
			&mockInsertDatabase{},
			bytes.NewReader([]byte("this is not really json in any way")),
			"user-identifier",
			http.StatusBadRequest,
			`{"error":"invalid character 'h' in literal true (expecting 'r')","status":400}`,
		},
		{
			"ok",
			&mockInsertDatabase{},
			bytes.NewReader([]byte(`{"accountId":"account-identifier","payload":"payload-value"}`)),
			"user-identifier",
			http.StatusOK,
			`{"ack":true}`,
		},
		{
			"database error",
			&mockInsertDatabase{err: errors.New("did not work")},
			bytes.NewReader([]byte(`{"accountId":"account-identifier","payload":"payload-value"}`)),
			"user-identifier",
			http.StatusInternalServerError,
			`{"error":"did not work","status":500}`,
		},
		{
			"account not found",
			&mockInsertDatabase{err: persistence.ErrUnknownAccount("unknown account")},
			bytes.NewReader([]byte(`{"accountId":"account-identifier","payload":"payload-value"}`)),
			"user-identifier",
			http.StatusNotFound,
			`{"error":"unknown account","status":404}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := router{
				db:                   test.db,
				logger:               nil,
				cookieSigner:         nil,
				secureCookie:         false,
				cookieExchangeSecret: nil,
				retentionPeriod:      time.Hour,
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", test.body)
			r = r.WithContext(context.WithValue(r.Context(), contextKeyCookie, test.userID))
			rt.postEvents(w, r)
			if w.Code != test.expectedStatusCode {
				t.Errorf("Expected status code %d, got %d", test.expectedStatusCode, w.Code)
			}
			if strings.Index(w.Body.String(), test.expectedResponse) == -1 {
				t.Errorf("Unexpected response body %s", w.Body.String())
			}
		})
	}

	t.Run("cookie renewal", func(t *testing.T) {
		rt := router{
			db:                   &mockInsertDatabase{},
			logger:               nil,
			cookieSigner:         nil,
			secureCookie:         true,
			cookieExchangeSecret: nil,
			retentionPeriod:      time.Hour,
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"accountId":"account-identifier","payload":"payload-value"}`)))
		r = r.WithContext(context.WithValue(r.Context(), contextKeyCookie, "user-token"))
		r.AddCookie(&http.Cookie{
			Name:    "user",
			Value:   "user-token",
			Expires: time.Now().Add(time.Hour),
			Secure:  true,
		})
		rt.postEvents(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
		}
		responseCookies := w.Result().Cookies()
		userCookie := responseCookies[0]
		if userCookie.Name != "user" {
			t.Errorf("Unexpected cookie name %s", userCookie.Name)
		}
		if userCookie.Expires.Before(time.Now().Add(time.Minute * 59)) {
			t.Errorf("Unexpected cookie expiry %s", userCookie.Expires)
		}
		if userCookie.Secure != true {
			t.Error("Expected secure cookie")
		}
	})
}

type mockQueryDatabase struct {
	persistence.Database
	payload string
	err     error
}

func (m *mockQueryDatabase) Query(q persistence.Query) (map[string][]persistence.EventResult, error) {
	out := map[string][]persistence.EventResult{}
	eventID := "event-id"
	if q.Since() != "" {
		eventID = q.Since()
	}
	for _, id := range q.AccountIDs() {
		userID := q.UserID()
		out[id] = append(out[id], persistence.EventResult{
			UserID:  &userID,
			Payload: m.payload,
			EventID: eventID,
		})
	}
	return out, m.err
}
func TestRouter_GetEvents(t *testing.T) {
	tests := []struct {
		name               string
		db                 persistence.Database
		queryString        string
		userID             interface{}
		expectedStatusCode int
		expectedResponse   string
	}{
		{
			"bad request context",
			&mockQueryDatabase{},
			"",
			[]string{"whoops"},
			http.StatusInternalServerError,
			`{"error":"could not use user id in request context","status":500}`,
		},
		{
			"database error",
			&mockQueryDatabase{err: errors.New("did not work")},
			"",
			"user-identifier",
			http.StatusInternalServerError,
			`{"error":"did not work","status":500}`,
		},
		{
			"no params",
			&mockQueryDatabase{payload: "payload-value"},
			"",
			"user-identifier",
			http.StatusOK,
			`{"events":{}}`,
		},
		{
			"query params",
			&mockQueryDatabase{payload: "payload-value"},
			"?accountId=account-identifier&accountId=other-identifier",
			"user-identifier",
			http.StatusOK,
			`{"events":{"account-identifier":[{"accountId":"","userId":"user-identifier","eventId":"event-id","payload":"payload-value"}],"other-identifier":[{"accountId":"","userId":"user-identifier","eventId":"event-id","payload":"payload-value"}]}}`,
		},
		{
			"since param",
			&mockQueryDatabase{payload: "payload-value"},
			"?accountId=account-identifier&since=since-value",
			"user-identifier",
			http.StatusOK,
			`{"events":{"account-identifier":[{"accountId":"","userId":"user-identifier","eventId":"since-value","payload":"payload-value"}]}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := router{
				db:                   test.db,
				logger:               nil,
				cookieSigner:         nil,
				secureCookie:         false,
				cookieExchangeSecret: nil,
				retentionPeriod:      time.Hour,
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%s", test.queryString), nil)
			r = r.WithContext(context.WithValue(r.Context(), contextKeyCookie, test.userID))
			rt.getEvents(w, r)
			if w.Code != test.expectedStatusCode {
				t.Errorf("Expected status code %d, got %d", test.expectedStatusCode, w.Code)
			}
			if strings.Index(w.Body.String(), test.expectedResponse) == -1 {
				t.Errorf("Unexpected response body %s", w.Body.String())
			}
		})
	}
}

type mockDeletedDatabase struct {
	persistence.Database
	result []string
	err    error
}

func (m *mockDeletedDatabase) GetDeletedEvents([]string, string) ([]string, error) {
	return m.result, m.err
}

func TestRouter_GetDeletedEvents(t *testing.T) {
	tests := []struct {
		name               string
		db                 persistence.Database
		body               string
		expectedStatusCode int
		expectedResponse   string
	}{
		{
			"bad payload",
			&mockDeletedDatabase{},
			`this-is-not-json`,
			http.StatusBadRequest,
			`{"error":"invalid character 'h' in literal true (expecting 'r')","status":400}`,
		},
		{
			"query error",
			&mockDeletedDatabase{
				err: errors.New("did not work"),
			},
			`{"eventIds":["a","b"]}`,
			http.StatusInternalServerError,
			`{"error":"did not work","status":500}`,
		},
		{
			"ok",
			&mockDeletedDatabase{
				result: []string{"b", "c"},
			},
			`{"eventIds":["a","b","c","d"]}`,
			http.StatusOK,
			`{"eventIds":["b","c"]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := router{
				db:                   test.db,
				logger:               nil,
				cookieSigner:         nil,
				secureCookie:         false,
				cookieExchangeSecret: nil,
				retentionPeriod:      time.Hour,
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(test.body)))

			rt.getDeletedEvents(w, r)
			if w.Code != test.expectedStatusCode {
				t.Errorf("Expected status code %d, got %d", test.expectedStatusCode, w.Code)
			}
			if strings.Index(w.Body.String(), test.expectedResponse) == -1 {
				t.Errorf("Unexpected response body %s", w.Body.String())
			}
		})
	}
}

type mockPurgeDatabase struct {
	persistence.Database
	err error
}

func (m *mockPurgeDatabase) Purge(string) error {
	return m.err
}

func TestRouter_PurgeEvents(t *testing.T) {
	tests := []struct {
		name               string
		db                 persistence.Database
		expectedStatusCode int
	}{
		{
			"persistence error",
			&mockPurgeDatabase{
				err: errors.New("did not work"),
			},
			http.StatusInternalServerError,
		},
		{
			"ok",
			&mockPurgeDatabase{},
			http.StatusNoContent,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := router{
				db:                   test.db,
				logger:               nil,
				cookieSigner:         nil,
				secureCookie:         false,
				cookieExchangeSecret: nil,
				retentionPeriod:      time.Hour,
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", nil)

			rt.purgeEvents(w, r)
			if w.Code != test.expectedStatusCode {
				t.Errorf("Expected status code %d, got %d", test.expectedStatusCode, w.Code)
			}
		})
	}
}
