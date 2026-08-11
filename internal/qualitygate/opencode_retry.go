package main

func retryableOpenCodeClassification(classification string) bool {
	switch classification {
	case "rate-limited", "infrastructure-model", "infrastructure-timeout":
		return true
	default:
		return false
	}
}

func combineOpenCodeAttempts(attempts []opencodeEvaluationResult) opencodeEvaluationResult {
	if len(attempts) == 0 {
		return opencodeEvaluationResult{Classification: "infrastructure-harness", Detail: "no-attempt", Variance: "invalid"}
	}
	result := attempts[len(attempts)-1]
	result.Attempts = len(attempts)
	result.Variance = classifyOpenCodeVariance(attempts)
	result.Duration = 0
	result.Cost = 0
	result.Tools = 0
	result.Steps = 0
	for _, attempt := range attempts {
		result.Duration += attempt.Duration
		result.Cost += attempt.Cost
		result.Tools += attempt.Tools
		result.Steps += attempt.Steps
	}
	return result
}

func classifyOpenCodeVariance(attempts []opencodeEvaluationResult) string {
	if len(attempts) < 2 {
		return "not-retried"
	}
	first := attempts[0]
	last := attempts[len(attempts)-1]
	if first.Classification == last.Classification && first.Detail == last.Detail {
		return "stable"
	}
	switch {
	case last.Classification == "pass":
		return "recovered-pass"
	case last.Classification == "rubric-failed":
		return "recovered-rubric"
	case retryableOpenCodeClassification(last.Classification):
		return "transient-variance"
	default:
		return "classification-variance"
	}
}
