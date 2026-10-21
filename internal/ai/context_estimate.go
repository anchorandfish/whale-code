package ai

import "encoding/json"

// RequestTokenEstimate describes a local estimate of the input context sent
// to a model. Provider usage is authoritative; these values are only used for
// UI guidance and local budget decisions.
type RequestTokenEstimate struct {
	SystemPrompt int
	Tools        int
	Messages     int
	Total        int
}

// EstimateRequestTokens estimates the input portion of a request without
// making a network call. It uses the same message representation as the API
// request, so multimodal blocks and tool results are included consistently.
func EstimateRequestTokens(estimator *TokenEstimator, req Request) RequestTokenEstimate {
	if estimator == nil {
		return RequestTokenEstimate{}
	}

	result := RequestTokenEstimate{}
	for _, prompt := range req.SystemPrompt {
		if prompt != "" {
			result.SystemPrompt += estimator.EstimateTextTokens(prompt) + 4
		}
	}
	result.Messages = estimator.EstimateMessagesTokens(req.Messages)

	if len(req.Tools) > 0 {
		if encoded, err := json.Marshal(req.Tools); err == nil {
			result.Tools = estimator.EstimateTextTokens(string(encoded)) + 4
		}
	}
	result.Total = result.SystemPrompt + result.Tools + result.Messages
	return result
}
