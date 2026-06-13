package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

type Options struct {
	Mode         string
	ClientID     string
	ClientSecret string
}

func TokenSource(ctx context.Context, opts Options) (oauth2.TokenSource, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Mode)) {
	case "", "loopback":
		return loopbackTokenSource(ctx, opts)
	case "adc":
		return adcTokenSource(ctx)
	default:
		return nil, fmt.Errorf("unknown auth mode %q (want loopback | adc)", opts.Mode)
	}
}

func adcTokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	creds, err := google.FindDefaultCredentials(ctx, CloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("Application Default Credentials not found — run `gcloud auth application-default login` or pass --auth=loopback: %w", err)
	}
	return creds.TokenSource, nil
}

func loopbackTokenSource(ctx context.Context, opts Options) (oauth2.TokenSource, error) {
	if opts.ClientID == "" {
		return nil, errors.New("--client-id is required for --auth=loopback (use a Desktop App OAuth client)")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback listener: %w", err)
	}
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())
	state := randomState()

	conf := &oauth2.Config{
		ClientID:     opts.ClientID,
		ClientSecret: opts.ClientSecret,
		RedirectURL:  redirect,
		Scopes:       []string{CloudPlatformScope},
		Endpoint:     google.Endpoint,
	}

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("oauth state mismatch (got %q)", got)}
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("oauth error: %s", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- result{err: errors.New("oauth callback missing code")}
			return
		}
		fmt.Fprintln(w, "Authorization complete — you can close this tab.")
		resultCh <- result{code: code}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	fmt.Fprintf(os.Stderr, "Open this URL to authorize:\n%s\n\n", authURL)
	_ = openBrowser(authURL)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := conf.Exchange(ctx, res.code)
		if err != nil {
			return nil, fmt.Errorf("token exchange: %w", err)
		}
		return conf.TokenSource(ctx, tok), nil
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for browser authorization")
	}
}

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("state-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}
