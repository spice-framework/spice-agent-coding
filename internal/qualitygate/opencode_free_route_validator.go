package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

type opencodeFreeRouteValidator struct {
	client *http.Client
}

func (validator opencodeFreeRouteValidator) newOpenCodeFreeRouteValidator() opencodeFreeRouteValidator {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("OpenCode evaluator requires the standard HTTP transport")
	}
	transport := baseTransport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return opencodeFreeRouteValidator{client: &http.Client{Timeout: 30 * time.Second, Transport: transport}}
}

func (validator opencodeFreeRouteValidator) Validate(ctx context.Context, expected []opencodeModel) (validateErr error) {
	catalog, err := validator.fetch(ctx)
	if err != nil {
		return err
	}
	available := make(map[string]opencodeOpenRouterModel, len(catalog.Data))
	for _, model := range catalog.Data {
		available[model.ID] = model
	}
	for _, wanted := range expected {
		if !(opencodeFreeRouteValidator{}).validOpenCodeFreeRoute(available[wanted.Route], wanted.Route) {
			return fmt.Errorf("OpenRouter route %s is not an exact zero-cost tool route", wanted.Label)
		}
	}
	return nil
}

func (validator opencodeFreeRouteValidator) fetch(ctx context.Context) (catalog opencodeOpenRouterResponse, fetchErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeFreeModelsEndpoint, nil)
	if err != nil {
		return catalog, fmt.Errorf("construct OpenRouter model request: %w", err)
	}
	response, err := validator.client.Do(request)
	if err != nil {
		return catalog, fmt.Errorf("query OpenRouter free-model catalog: %w", err)
	}
	defer func() {
		fetchErr = errors.Join(fetchErr, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximumOpenCodeCatalogBytes {
		return catalog, fmt.Errorf("query OpenRouter free-model catalog returned HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumOpenCodeCatalogBytes+1))
	if err != nil {
		return catalog, fmt.Errorf("read OpenRouter free-model catalog: %w", err)
	}
	if len(content) > maximumOpenCodeCatalogBytes {
		return catalog, errors.New("OpenRouter free-model catalog exceeds its byte cap")
	}
	if err = json.Unmarshal(content, &catalog); err != nil {
		return catalog, fmt.Errorf("decode OpenRouter free-model catalog: %w", err)
	}
	if len(catalog.Data) == 0 {
		return catalog, errors.New("OpenRouter free-model catalog is empty")
	}
	return catalog, nil
}

func (validator opencodeFreeRouteValidator) validOpenCodeFreeRoute(model opencodeOpenRouterModel, expected string) bool {
	return model.ID == expected && strings.HasSuffix(model.ID, ":free") && model.Pricing.Prompt == "0" &&
		model.Pricing.Completion == "0" && slices.Contains(model.SupportedParameters, "max_tokens") &&
		slices.Contains(model.SupportedParameters, "tools") && slices.Contains(model.SupportedParameters, "tool_choice")
}
