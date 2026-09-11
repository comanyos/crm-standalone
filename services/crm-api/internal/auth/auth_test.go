package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
)

var anyProcedurePolicy = ScopePolicy{
	Unscoped: map[string]string{"": "a request built in a unit test has no procedure name"},
}

func callInterceptor(t *testing.T, a *Authenticator, policy ScopePolicy, headers map[string]string) (*Principal, error) {
	t.Helper()
	var seen *Principal
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen, _ = FromContext(ctx)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	req := connect.NewRequest(&emptypb.Empty{})
	for key, value := range headers {
		req.Header().Set(key, value)
	}
	_, err := a.Interceptor(policy)(next)(context.Background(), req)
	return seen, err
}

func TestInterceptorAcceptsOpaqueBearerAndCookie(t *testing.T) {
	want := &Principal{Subject: "usr_1", TenantID: "org_1", OrgID: "org_1", Roles: []string{"crm_admin"}, Scopes: []string{"crm:*"}}
	a := NewTestingAuthenticator(func(_ context.Context, token string) (*Principal, error) {
		if token != "opaque-session" {
			return nil, ErrInvalidSession
		}
		return want, nil
	}, nil)
	for name, headers := range map[string]map[string]string{
		"bearer": {"Authorization": "Bearer opaque-session"},
		"cookie": {"Cookie": "savvy_session=opaque-session"},
	} {
		t.Run(name, func(t *testing.T) {
			seen, err := callInterceptor(t, a, anyProcedurePolicy, headers)
			if err != nil || seen != want {
				t.Fatalf("principal=%+v err=%v", seen, err)
			}
		})
	}
}

func TestInterceptorFailsClosed(t *testing.T) {
	a := NewTestingAuthenticator(func(context.Context, string) (*Principal, error) {
		return nil, ErrInvalidSession
	}, nil)
	if _, err := callInterceptor(t, a, anyProcedurePolicy, nil); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing session code=%v", connect.CodeOf(err))
	}
	if _, err := callInterceptor(t, a, anyProcedurePolicy, map[string]string{"Authorization": "Bearer bad"}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("invalid session code=%v", connect.CodeOf(err))
	}
	unknown := ScopePolicy{Required: map[string]string{"/something-else": "crm:x:read"}}
	if _, err := callInterceptor(t, NewTestingAuthenticator(func(context.Context, string) (*Principal, error) {
		return &Principal{Scopes: []string{"crm:*"}}, nil
	}, nil), unknown, map[string]string{"Authorization": "Bearer good"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("unknown procedure code=%v", connect.CodeOf(err))
	}
}

func TestScopeAndPrincipalHelpers(t *testing.T) {
	p := &Principal{Subject: "usr", Scopes: []string{"crm:*"}}
	if !p.HasScope("crm:anything:write") {
		t.Fatal("crm:* did not grant a CRM scope")
	}
	ctx := WithPrincipal(context.Background(), p)
	if got, ok := FromContext(ctx); !ok || got != p {
		t.Fatalf("context principal=%+v ok=%v", got, ok)
	}
	if bearer("bearer token") != "token" || bearer("Basic token") != "" {
		t.Fatal("Bearer parsing is incorrect")
	}
}

func TestAuthenticateRequestSupportsCookie(t *testing.T) {
	want := &Principal{Subject: "usr", TenantID: "org"}
	a := NewTestingAuthenticator(func(_ context.Context, token string) (*Principal, error) {
		if token == "opaque" {
			return want, nil
		}
		return nil, ErrInvalidSession
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.AddCookie(&http.Cookie{Name: defaultCookieName, Value: "opaque"})
	ctx, err := a.AuthenticateRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := FromContext(ctx); got != want {
		t.Fatalf("principal=%+v", got)
	}
	if _, err := a.AuthenticateRequest(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("missing raw HTTP session was accepted")
	}
}

func TestReadinessDelegatesAndDisabledIsExplicit(t *testing.T) {
	want := errors.New("database unavailable")
	a := NewTestingAuthenticator(nil, func(context.Context) error { return want })
	if err := a.Ready(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ready err=%v", err)
	}
	if err := Disabled().Ready(context.Background()); err != nil {
		t.Fatalf("disabled test auth readiness=%v", err)
	}
}
