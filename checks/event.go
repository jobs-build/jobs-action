package checks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// HeadSHA is the commit a check run attaches to.
//
// On a pull_request event GITHUB_SHA is the merge commit, which no pull
// request page shows, so the check goes on the pull request's head commit,
// read from the event payload at eventPath. Every other event uses sha.
//
// pull_request_target is deliberately not special: its default checkout is
// the base branch, and results attached to the pull request's head would be
// green checks on code that was never tested.
func HeadSHA(eventName, eventPath, sha string) (string, error) {
	if eventName != "pull_request" {
		if sha == "" {
			return "", errors.New("GITHUB_SHA is empty")
		}
		return sha, nil
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		return "", fmt.Errorf("read the pull_request event payload: %w", err)
	}
	var ev struct {
		PullRequest struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return "", fmt.Errorf("decode the pull_request event payload: %w", err)
	}
	if ev.PullRequest.Head.SHA == "" {
		return "", errors.New("the pull_request event payload has no pull_request.head.sha")
	}
	return ev.PullRequest.Head.SHA, nil
}
