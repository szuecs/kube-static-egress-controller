package auth

import (
	"fmt"
	"net/http"
	"os"
	"path"

	"golang.org/x/oauth2"
	"k8s.io/client-go/transport"
)

const (
	DefaultCredentialsDir = "/meta/credentials"
	CredentialsDirEnvar   = "CREDENTIALS_DIR"
)

type platformCredentialsTokenSource struct {
	tokenName      string
	credentialsDir string
}

func NewPlatformCredentialsTokenSource(tokenName string, credentialsDir string) *platformCredentialsTokenSource {
	return &platformCredentialsTokenSource{
		tokenName:      tokenName,
		credentialsDir: credentialsDir,
	}
}

func (s *platformCredentialsTokenSource) Token() (*oauth2.Token, error) {
	filePath := path.Join(s.credentialsDir, fmt.Sprintf("%s-token-secret", s.tokenName))
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: string(contents)}, nil
}

type tokenInjector struct {
	tokenSource oauth2.TokenSource
	next        http.RoundTripper
}

func TokenInjector(tokenSource oauth2.TokenSource) transport.WrapperFunc {
	return func(rt http.RoundTripper) http.RoundTripper {
		return &tokenInjector{
			tokenSource: tokenSource,
			next:        rt,
		}
	}
}

func (i *tokenInjector) RoundTrip(request *http.Request) (*http.Response, error) {
	token, err := i.tokenSource.Token()
	if err != nil {
		return nil, err
	}

	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	return i.next.RoundTrip(request)
}
