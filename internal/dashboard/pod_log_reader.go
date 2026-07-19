package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// execReadActivityLog execs into the sandbox pod and reads /workspace/events.jsonl.
func execReadActivityLog(ctx context.Context, clientset *kubernetes.Clientset, restConfig *rest.Config, podName, namespace string) ([]*platform.ActivityEntry, error) {
	evOut, err := execInPodFunc(ctx, clientset, restConfig, podName, namespace,
		[]string{"cat", "/workspace/events.jsonl"})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(evOut) == "" {
		return nil, nil
	}
	return parseEventStream(evOut)
}

// parseEventStream parses events.jsonl content into ActivityEntry protos.
func parseEventStream(data string) ([]*platform.ActivityEntry, error) {
	var entries []*platform.ActivityEntry
	reader := bufio.NewReader(strings.NewReader(data))
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}
		var ev agent.ContentEvent
		if jsonErr := json.Unmarshal(line, &ev); jsonErr != nil {
			log.Printf("WARN: skipping malformed event line from pod exec: %v", jsonErr)
			if err == io.EOF {
				break
			}
			continue
		}
		entry := contentEventToActivityEntry(&ev)
		preserveEventUsageCacheSemantics(line, entry)
		entries = append(entries, entry)
		if err == io.EOF {
			break
		}
	}
	return entries, nil
}
