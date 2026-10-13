package tests

import "time"

func commitAcknowledgement(a Attempt, ids []string) map[string]any {
	return map[string]any{"acknowledged_commit_ids": ids, "revision": a.Revision, "server_answer_count": len(a.Answers), "server_time": time.Now().UTC().Format(time.RFC3339Nano)}
}
