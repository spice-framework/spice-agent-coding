// Package openaiprovider provides the instance-owned OpenAI Responses client
// used by the Spice Agent model adapter.
package openaiprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	defaultTimeout = 2 * time.Minute
	maximumTimeout = 30 * time.Minute
	maximumRetries = 8
)

// Config is the typed, secret-aware configuration for one provider instance.
// APIKey is deliberately excluded from String output and validation errors.
// BaseURL requires HTTPS except for explicit localhost or IP-loopback HTTP test
// bridges; loopback HTTP provides no transport security.
// A zero Timeout selects two minutes; explicit values must not exceed thirty
// minutes.
type Config struct {
	APIKey       string        `spice:"api-key,required,secret,env=OPENAI_API_KEY"`
	BaseURL      string        `spice:"base-url,default=https://api.openai.com/v1,env=OPENAI_BASE_URL"`
	Organization string        `spice:"organization,env=OPENAI_ORGANIZATION"`
	Project      string        `spice:"project,env=OPENAI_PROJECT"`
	Timeout      time.Duration `spice:"timeout,default=2m,env=OPENAI_TIMEOUT"`
	MaxRetries   int           `spice:"max-retries,default=0,env=OPENAI_MAX_RETRIES"`
}

// String returns a diagnostic-safe summary.
func (config Config) String() string {
	return fmt.Sprintf(
		"OpenAI(base_url=%q, organization=%q, project=%q, timeout=%s, max_retries=%d, api_key=<redacted>)",
		normalizedBaseURL(config.BaseURL),
		config.Organization,
		config.Project,
		normalizedTimeout(config.Timeout),
		config.MaxRetries,
	)
}

// Client owns one configured OpenAI Responses service. Construction performs
// no network I/O and never consults ambient OpenAI environment variables.
type Client struct {
	start streamStarter
}

type responseSource interface {
	Next() bool
	Current() responses.ResponseStreamEventUnion
	Err() error
	Close() error
}

type streamStarter func(context.Context, responses.ResponseNewParams, ...option.RequestOption) responseSource

// ClientOption customizes client construction without adding hidden globals.
type ClientOption func(*clientOptions) error

type clientOptions struct {
	httpClient *http.Client
}

// WithHTTPClient supplies an instance-owned HTTP client, typically for tests or
// application-specific transport policy.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(options *clientOptions) error {
		if client == nil {
			return errors.New("OpenAI HTTP client must not be nil")
		}
		options.httpClient = client
		return nil
	}
}

// New validates configuration and constructs an instance-owned Responses
// client without making a request.
func New(config Config, options ...ClientOption) (*Client, error) {
	config = normalize(config)
	if err := config.validate(); err != nil {
		return nil, err
	}
	construction := clientOptions{httpClient: &http.Client{Timeout: config.Timeout}}
	for index, apply := range options {
		if apply == nil {
			return nil, fmt.Errorf("OpenAI client option %d is nil", index)
		}
		if err := apply(&construction); err != nil {
			return nil, fmt.Errorf("apply OpenAI client option %d: %w", index, err)
		}
	}
	requestOptions := []option.RequestOption{
		option.WithAPIKey(config.APIKey),
		option.WithBaseURL(config.BaseURL),
		option.WithHTTPClient(construction.httpClient),
		option.WithRequestTimeout(config.Timeout),
		option.WithMaxRetries(config.MaxRetries),
	}
	if config.Organization != "" {
		requestOptions = append(requestOptions, option.WithOrganization(config.Organization))
	}
	if config.Project != "" {
		requestOptions = append(requestOptions, option.WithProject(config.Project))
	}
	service := responses.NewResponseService(requestOptions...)
	return &Client{start: func(ctx context.Context, params responses.ResponseNewParams, options ...option.RequestOption) responseSource {
		return service.NewStreaming(ctx, params, options...)
	}}, nil
}

var _ responseSource = (*ssestream.Stream[responses.ResponseStreamEventUnion])(nil)

func normalize(config Config) Config {
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = normalizedBaseURL(config.BaseURL)
	config.Organization = strings.TrimSpace(config.Organization)
	config.Project = strings.TrimSpace(config.Project)
	config.Timeout = normalizedTimeout(config.Timeout)
	return config
}

func normalizedBaseURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(value, "/")
}

func normalizedTimeout(value time.Duration) time.Duration {
	if value == 0 {
		return defaultTimeout
	}
	return value
}

func (config Config) validate() error {
	if config.APIKey == "" {
		return errors.New("OpenAI API key is required")
	}
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || !allowedBaseURLScheme(endpoint) {
		return errors.New("OpenAI base URL must be an absolute HTTPS URL without user information (HTTP is allowed only for loopback test endpoints)")
	}
	if config.Timeout <= 0 || config.Timeout > maximumTimeout {
		return fmt.Errorf("OpenAI timeout must be positive and no greater than %s", maximumTimeout)
	}
	if config.MaxRetries < 0 || config.MaxRetries > maximumRetries {
		return fmt.Errorf("OpenAI max retries must be between 0 and %d", maximumRetries)
	}
	return nil
}

// allowedBaseURLScheme accepts production HTTPS endpoints and loopback-only
// HTTP for local test bridges. Localhost is retained as an explicit conventional
// alias; literal addresses are parsed and must belong to an IP loopback range.
func allowedBaseURLScheme(endpoint *url.URL) bool {
	switch strings.ToLower(endpoint.Scheme) {
	case "https":
		return true
	case "http":
		host := strings.ToLower(endpoint.Hostname())
		if host == "localhost" {
			return true
		}
		address, err := netip.ParseAddr(host)
		return err == nil && address.Unmap().IsLoopback()
	default:
		return false
	}
}
