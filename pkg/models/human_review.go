package models

// HumanReviewRequest is the task value inserted into a human_review queue
// when an agent needs human judgment, approval, or action before proceeding.
// The task is inserted by the requesting agent; the reply_queue tells the
// human reviewer where to post the outcome.
type HumanReviewRequest struct {
	SessionURI      string `json:"session_uri"`       // doc:sessions/<id>
	RequestingAgent string `json:"requesting_agent"`   // agent that needs review
	Reason          string `json:"reason"`             // why human input is needed
	ReplyQueue      string `json:"reply_queue"`        // where to post HumanReviewReply
	ContextSummary  string `json:"context_summary"`    // brief description for the reviewer
}

// HumanReviewReply is the task value posted back into reply_queue after the
// human has acted. The receiving agent uses outcome and human_input to decide
// how to continue the session.
type HumanReviewReply struct {
	SessionURI   string `json:"session_uri"`    // same as the original request
	Outcome      string `json:"outcome"`        // "approved", "rejected", "input_provided"
	HumanInput   string `json:"human_input"`    // free-form text from the human
	ReviewTaskID string `json:"review_task_id"` // ID of the human_review task that was handled
}
