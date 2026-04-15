package cmd

import (
	"time"

	"github.com/shiblon/entroq"
)

// retryDelay returns an exponential backoff delay capped at 5 minutes,
// based on the number of prior attempts on the task.
func retryDelay(attempts int32) time.Duration {
	d := time.Duration(attempts+1) * 30 * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// retryMod returns a ModifyArg that puts the task back with an incremented
// attempt counter, the error message appended to the task's error log, and
// a backoff delay before it becomes claimable again.
func retryMod(task *entroq.Task, errMsg string) entroq.ModifyArg {
	return entroq.Changing(task,
		entroq.AppendingErr(errMsg),
		entroq.AttemptToNext(),
		entroq.ArrivalTimeBy(retryDelay(task.Attempt)),
	)
}
