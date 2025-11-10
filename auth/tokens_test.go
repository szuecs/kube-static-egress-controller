package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestPlatformCredentialsTokenSource(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "credentials")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	tempFile := filepath.Join(tempDir, "my-app-token-secret")
	err = os.WriteFile(tempFile, []byte("the-token"), 0400)
	require.NoError(t, err)

	tokenSource := NewPlatformCredentialsTokenSource("my-app", tempDir)

	token, err := tokenSource.Token()
	require.NoError(t, err)

	assert.Equal(t, "the-token", token.AccessToken)
}

func TestTokenInjector(t *testing.T) {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "the-token"})
	http.DefaultTransport = TokenInjector(tokenSource)(http.DefaultTransport)

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer svr.Close()

	res, err := http.Get(svr.URL)
	require.NoError(t, err)
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	assert.Equal(t, "Bearer the-token", string(out))
}
