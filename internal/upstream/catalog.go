// Package upstream provides the authenticated, bounded HTTP transport.
package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/catalog"
	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/jpetrucciani/ollame/internal/obs"
)

var (
	ErrUnavailable      = errors.New("upstream unavailable")
	ErrResponseTooLarge = errors.New("upstream response too large")
	ErrCredentials      = errors.New("upstream key file unreadable or empty")
	ErrInvalidResponse  = errors.New("upstream returned invalid catalog")
)

type Client struct {
	http      *http.Client
	transport *http.Transport
	config    config.Upstream
	key       string
	metrics   *obs.Metrics
}

func ReadKey(cfg config.Upstream) (string, error) {
	if cfg.APIKeyFile == "" {
		return cfg.APIKey, nil
	}
	file, err := os.Open(cfg.APIKeyFile)
	if err != nil {
		return "", ErrCredentials
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(bytes) > 65536 {
		return "", ErrCredentials
	}
	value := strings.TrimSpace(string(bytes))
	if value == "" {
		return "", ErrCredentials
	}
	return value, nil
}
func New(cfg config.Upstream, key string, metrics *obs.Metrics, tracing *obs.Tracing) (*Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify}
	if cfg.CAFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle: %w", ErrUnavailable)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid CA bundle: %w", ErrUnavailable)
		}
		tlsConfig.RootCAs = roots
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: time.Duration(cfg.ConnectTimeout), KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, TLSClientConfig: tlsConfig, TLSHandshakeTimeout: time.Duration(cfg.ConnectTimeout), ResponseHeaderTimeout: time.Duration(cfg.HeaderTimeout), MaxIdleConns: cfg.MaxIdleConns, MaxIdleConnsPerHost: cfg.MaxIdleConns, IdleConnTimeout: 90 * time.Second}
	// Redirects must never move a model request or credential outside this client.
	var roundTripper http.RoundTripper = transport
	if tracing != nil {
		roundTripper = tracing.Transport(transport)
	}
	client := &http.Client{Transport: roundTripper, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{http: client, transport: transport, config: cfg, key: key, metrics: metrics}, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }

func (c *Client) get(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	endpoint, err := url.JoinPath(c.config.BaseURL, path)
	if err != nil {
		return nil, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	for key, value := range c.config.ExtraHeaders {
		request.Header.Set(key, value)
	}
	request.Header.Del("Authorization")
	request.Header.Del("X-Api-Key")
	request.Header.Del("Cookie")
	if c.key != "" {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	c.recordAttempt(path, response)
	if err != nil {
		return nil, fmt.Errorf("%w: catalog request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: catalog HTTP %d", ErrUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: catalog body failed", ErrUnavailable)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}
func (c *Client) Discover(ctx context.Context, cfg config.Config) ([]catalog.Model, []string, error) {
	fetch, cancel := context.WithTimeout(ctx, time.Duration(cfg.Models.FetchTimeout))
	data, err := c.get(fetch, "models", int64(cfg.Limits.MaxUpstreamJSONBytes))
	cancel()
	if err != nil {
		return nil, nil, err
	}
	var list struct {
		Data []catalog.Model `json:"data"`
	}
	if err = json.Unmarshal(data, &list); err != nil || list.Data == nil {
		return nil, nil, ErrInvalidResponse
	}
	if len(list.Data) > cfg.Limits.MaxCatalogModels {
		return nil, nil, ErrResponseTooLarge
	}
	warnings := []string{}
	if cfg.Upstream.Flavor != "litellm" || cfg.Upstream.ModelInfo == "off" {
		return list.Data, warnings, nil
	}
	fetch, cancel = context.WithTimeout(ctx, time.Duration(cfg.Models.FetchTimeout))
	defer cancel()
	data, err = c.get(fetch, "model/info", int64(cfg.Limits.MaxUpstreamJSONBytes))
	if err != nil {
		if cfg.Upstream.ModelInfo == "litellm" {
			warnings = append(warnings, "optional LiteLLM metadata unavailable")
		}
		return list.Data, warnings, nil
	}
	var metadata struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				Mode              string `json:"mode"`
				MaxInputTokens    int    `json:"max_input_tokens"`
				MaxTokens         int    `json:"max_tokens"`
				SupportsTools     *bool  `json:"supports_function_calling"`
				SupportsVision    *bool  `json:"supports_vision"`
				SupportsReasoning *bool  `json:"supports_reasoning"`
				OutputVectorSize  int    `json:"output_vector_size"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err = json.Unmarshal(data, &metadata); err != nil || metadata.Data == nil {
		if cfg.Upstream.ModelInfo == "litellm" {
			warnings = append(warnings, "optional LiteLLM metadata invalid")
		}
		return list.Data, warnings, nil
	}
	info := map[string]catalog.Info{}
	for _, entry := range metadata.Data {
		m := entry.ModelInfo
		info[entry.ModelName] = catalog.Info{Mode: m.Mode, MaxInputTokens: m.MaxInputTokens, MaxTokens: m.MaxTokens, SupportsTools: m.SupportsTools, SupportsVision: m.SupportsVision, SupportsReasoning: m.SupportsReasoning, OutputVectorSize: m.OutputVectorSize}
	}
	for i := range list.Data {
		list.Data[i].Info = info[list.Data[i].ID]
	}
	return list.Data, warnings, nil
}
