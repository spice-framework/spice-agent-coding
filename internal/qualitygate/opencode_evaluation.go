package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type opencodeEvaluation struct {
	catalog       opencodeCatalog
	bootstrapper  opencodeBootstrapper
	configuration opencodeConfiguration
	credential    opencodeCredential
	freeRoutes    opencodeFreeRouteValidator
	invocation    opencodeInvocation
	rubric        opencodeRubric
	tree          opencodeTree
}

func newOpenCodeEvaluation() opencodeEvaluation {
	return opencodeEvaluation{
		catalog: opencodeCatalog{}, bootstrapper: newOpenCodeBootstrapper(), configuration: newOpenCodeConfiguration(),
		credential: opencodeCredential{}, freeRoutes: newOpenCodeFreeRouteValidator(), invocation: opencodeInvocation{},
		rubric: opencodeRubric{}, tree: opencodeTree{},
	}
}

func (evaluation opencodeEvaluation) Run(ctx context.Context, source string, destination io.Writer) (runErr error) {
	if destination == nil || !filepath.IsAbs(source) {
		return errors.New("OpenCode evaluation requires an absolute repository and output")
	}
	evaluationContext, cancel := context.WithTimeout(ctx, maximumOpenCodeEvaluationDuration)
	defer cancel()
	workspace, err := newOpenCodeWorkspace()
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, workspace.Close())
	}()
	executable, environment, models, err := evaluation.prepare(evaluationContext, workspace)
	if err != nil {
		return err
	}
	results, err := evaluation.evaluateMatrix(evaluationContext, source, workspace, executable, environment, models)
	if err != nil {
		return err
	}
	return writeOpenCodeResults(destination, results)
}

func (evaluation opencodeEvaluation) prepare(
	ctx context.Context,
	workspace *opencodeWorkspace,
) (string, []string, []opencodeModel, error) {
	configurationContent, err := evaluation.configuration.Encode()
	if err != nil {
		return "", nil, nil, err
	}
	if err = os.WriteFile(workspace.config, configurationContent, 0o600); err != nil { // #nosec G306 -- temporary config contains no credential.
		return "", nil, nil, fmt.Errorf("write isolated OpenCode configuration: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, nil, errors.New("resolve configured OpenCode credential home")
	}
	if err = evaluation.credential.Copy(evaluation.credential.SourcePath(home), workspace.auth); err != nil {
		return "", nil, nil, err
	}
	environment := newOpenCodeEnvironment(workspace.root, workspace.config, string(configurationContent)).Values()
	executable, err := evaluation.bootstrapper.Install(ctx, workspace.root)
	if err != nil {
		return "", nil, nil, fmt.Errorf("OpenCode advisory infrastructure unavailable: %w", err)
	}
	models := evaluation.catalog.Models()
	if err = evaluation.freeRoutes.Validate(ctx, models); err != nil {
		return "", nil, nil, fmt.Errorf("OpenCode advisory routes unavailable: %w", err)
	}
	return executable, environment, models, nil
}

func (evaluation opencodeEvaluation) evaluateMatrix(
	ctx context.Context,
	source string,
	workspace *opencodeWorkspace,
	executable string,
	environment []string,
	models []opencodeModel,
) ([]opencodeEvaluationResult, error) {
	validatedConfiguration := false
	results := make([]opencodeEvaluationResult, 0, len(models)*len(openCodeEvaluationCases()))
	for modelIndex, model := range models {
		for caseIndex, evaluationCase := range openCodeEvaluationCases() {
			label := fmt.Sprintf("%02d-%02d-%s-%s", modelIndex, caseIndex, model.Label, evaluationCase.Name)
			repositoryRoot, pathErr := workspace.CaseRepository(label)
			if pathErr != nil {
				return nil, pathErr
			}
			repository, constructErr := newOpenCodeRepository(source, repositoryRoot)
			if constructErr != nil {
				return nil, constructErr
			}
			if copyErr := repository.Copy(ctx); copyErr != nil {
				return nil, copyErr
			}
			if !validatedConfiguration {
				if validateErr := evaluation.validateConfiguration(ctx, executable, environment, repositoryRoot); validateErr != nil {
					return nil, validateErr
				}
				validatedConfiguration = true
			}
			result, executeErr := evaluation.runCase(
				ctx, executable, environment, repository, model, evaluationCase,
			)
			if executeErr != nil {
				return nil, executeErr
			}
			results = append(results, result)
			if result.Classification == "safety-failed" {
				return nil, fmt.Errorf(
					"OpenCode advisory evaluation violated its safety contract: model=%s case=%s detail=%s before=%s after=%s",
					result.Model, result.Case, result.SafetyDetail, result.Before, result.After,
				)
			}
		}
	}
	return results, nil
}

func writeOpenCodeResults(destination io.Writer, results []opencodeEvaluationResult) error {
	if _, err := fmt.Fprintf(destination, "OpenCode advisory matrix (%s; exact free routes; no raw model output)\n", openCodeReviewedWorkflowPackageVersion); err != nil {
		return err
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(
			destination,
			"  model=%s case=%s classification=%s duration=%s cost=%.4f tools=%d steps=%d before=%s after=%s\n",
			result.Model, result.Case, result.Classification, result.Duration.Round(time.Millisecond), result.Cost,
			result.Tools, result.Steps, result.Before, result.After,
		); err != nil {
			return err
		}
	}
	return nil
}

func (evaluation opencodeEvaluation) validateConfiguration(
	ctx context.Context,
	executable string,
	environment []string,
	repository string,
) error {
	resolved, err := boundedCommandOutput(
		ctx, repository, environment, maximumOpenCodeEventBytes, executable, "debug", "config", "--pure",
	)
	if err != nil {
		return fmt.Errorf("resolve isolated OpenCode configuration: %w", err)
	}
	return evaluation.configuration.ValidateResolved([]byte(resolved))
}

func (evaluation opencodeEvaluation) runCase(
	ctx context.Context,
	executable string,
	environment []string,
	repository opencodeRepository,
	model opencodeModel,
	evaluationCase opencodeCase,
) (opencodeEvaluationResult, error) {
	pristine, err := evaluation.tree.Snapshot(repository.target)
	if err != nil {
		return opencodeEvaluationResult{}, err
	}
	var original []byte
	if evaluationCase.Name == "seeded-defect" {
		original, err = (opencodeSeededDefect{}).Apply(repository)
		if err != nil {
			return opencodeEvaluationResult{}, err
		}
	}
	before, err := evaluation.tree.Snapshot(repository.target)
	if err != nil {
		return opencodeEvaluationResult{}, err
	}
	summary, duration, classification := evaluation.invocation.Run(
		ctx, executable, environment, repository.target, model, evaluationCase,
	)
	after, err := evaluation.tree.Snapshot(repository.target)
	if err != nil {
		return opencodeEvaluationResult{}, err
	}
	if classification == "" {
		classification = evaluation.rubric.Evaluate(ctx, repository, evaluationCase, pristine, before, after, original, summary)
	}
	safetyDetail := "none"
	if classification == "safety-failed" {
		safetyDetail = "deterministic-rubric"
		if summary.SafetyFailure != "" {
			safetyDetail = openCodeSafetyDetail(summary.SafetyFailure)
		}
	}
	return opencodeEvaluationResult{
		Model: model.Label, Case: evaluationCase.Name, Classification: classification, SafetyDetail: safetyDetail, Duration: duration,
		Cost: summary.Cost, Tools: len(summary.Tools), Steps: summary.Steps,
		Before: shortOpenCodeDigest(before.HexDigest()), After: shortOpenCodeDigest(after.HexDigest()),
	}, nil
}

func openCodeSafetyDetail(value string) string {
	switch value {
	case "event output cap exceeded":
		return "event-output-cap"
	case "invalid OpenCode event stream", "invalid trailing OpenCode event":
		return "invalid-event-stream"
	case "cost or step cap exceeded":
		return "cost-or-step-cap"
	case "tool cap exceeded":
		return "tool-cap"
	case "model text cap exceeded":
		return "model-text-cap"
	case "diagnostic output cap exceeded":
		return "diagnostic-output-cap"
	default:
		return "event-safety"
	}
}

func shortOpenCodeDigest(value string) string {
	if len(value) < 16 || strings.ContainsAny(value, "\r\n\x00") {
		return "invalid"
	}
	return value[:16]
}
